package selfupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/utils"
)

const (
	updateRootName = ".komari-update"
	lastResultName = "last-result.json"
)

type HelperConfig struct {
	JobID               string        `json:"job_id"`
	CurrentExecutable   string        `json:"current_executable"`
	CandidateExecutable string        `json:"candidate_executable"`
	DataDir             string        `json:"data_dir"`
	Service             string        `json:"service"`
	HealthURL           string        `json:"health_url"`
	ExpectedVersion     string        `json:"expected_version"`
	ExpectedHash        string        `json:"expected_hash"`
	PreviousVersion     string        `json:"previous_version"`
	PreviousHash        string        `json:"previous_hash"`
	UpdateRoot          string        `json:"update_root"`
	BackupRoot          string        `json:"backup_root"`
	StartDelay          time.Duration `json:"start_delay"`
	HealthTimeout       time.Duration `json:"health_timeout"`
	StableWindow        time.Duration `json:"stable_window"`
}

type UpdateResult struct {
	JobID          string    `json:"job_id"`
	Status         string    `json:"status"`
	TargetVersion  string    `json:"target_version"`
	TargetHash     string    `json:"target_hash"`
	CurrentVersion string    `json:"current_version,omitempty"`
	CurrentHash    string    `json:"current_hash,omitempty"`
	Message        string    `json:"message,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type candidateVersion struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

var prepareMu sync.Mutex

func PrepareAndLaunch(ctx context.Context, version, versionHash string) (*UpdateResult, error) {
	prepareMu.Lock()
	defer prepareMu.Unlock()
	version = strings.TrimSpace(version)
	versionHash = strings.TrimSpace(versionHash)
	if !versionPattern.MatchString(version) || !hashPattern.MatchString(versionHash) {
		return nil, errors.New("invalid update version or build identifier")
	}
	capability := DetectCapability()
	if !capability.Supported {
		return nil, fmt.Errorf("self update is unavailable: %s", capability.Reason)
	}
	executable, dataDir, err := managedPaths()
	if err != nil {
		return nil, err
	}
	if previous, err := ReadLastResult(filepath.Dir(executable)); err == nil && previous != nil && isUpdateInProgress(previous.Status) && time.Since(previous.UpdatedAt) < 15*time.Minute {
		return nil, errors.New("another self-update transaction is already running")
	}
	healthURL, err := localHealthURL()
	if err != nil {
		return nil, err
	}

	jobID, err := randomID()
	if err != nil {
		return nil, err
	}
	updateRoot := filepath.Join(filepath.Dir(executable), updateRootName)
	jobRoot := filepath.Join(updateRoot, "jobs", jobID)
	if err := os.MkdirAll(jobRoot, 0700); err != nil {
		return nil, err
	}
	launched := false
	defer func() {
		if !launched {
			_ = os.RemoveAll(jobRoot)
		}
	}()
	candidate := filepath.Join(jobRoot, "komari-candidate")
	client := updateHTTPClient()
	manifest, err := fetchManifest(releaseURL(version, manifestName), client)
	if err != nil {
		return nil, err
	}
	asset, err := manifest.validate(version, versionHash)
	if err != nil {
		return nil, err
	}
	if err := downloadAsset(client, releaseURL(version, asset.Name), candidate, *asset); err != nil {
		return nil, err
	}
	if err := verifyCandidate(ctx, candidate, version, versionHash); err != nil {
		return nil, err
	}

	backupRoot := filepath.Join(filepath.Dir(executable), "backup", "self-update-"+time.Now().Format("20060102-150405")+"-"+jobID)
	config := HelperConfig{
		JobID:               jobID,
		CurrentExecutable:   executable,
		CandidateExecutable: candidate,
		DataDir:             dataDir,
		Service:             capability.Service,
		HealthURL:           healthURL,
		ExpectedVersion:     version,
		ExpectedHash:        versionHash,
		PreviousVersion:     utils.CurrentVersion,
		PreviousHash:        utils.VersionHash,
		UpdateRoot:          updateRoot,
		BackupRoot:          backupRoot,
		StartDelay:          3 * time.Second,
		HealthTimeout:       90 * time.Second,
		StableWindow:        15 * time.Second,
	}
	configPath := filepath.Join(jobRoot, "helper.json")
	if err := atomicWriteJSON(configPath, config, 0600); err != nil {
		return nil, err
	}

	result := &UpdateResult{
		JobID:         jobID,
		Status:        "scheduled",
		TargetVersion: version,
		TargetHash:    versionHash,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := atomicWriteJSON(filepath.Join(updateRoot, lastResultName), result, 0600); err != nil {
		return nil, err
	}

	command := exec.CommandContext(ctx, "systemd-run",
		"--unit=komari-self-update-"+jobID,
		"--no-block",
		"--property=Restart=on-failure",
		"--property=RestartSec=3s",
		candidate, "_self-update-helper", configPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		result.Status = "failed"
		result.Message = strings.TrimSpace(string(output))
		result.UpdatedAt = time.Now().UTC()
		_ = atomicWriteJSON(filepath.Join(updateRoot, lastResultName), result, 0600)
		return nil, fmt.Errorf("failed to schedule update helper: %w: %s", err, output)
	}
	launched = true
	return result, nil
}

func isUpdateInProgress(status string) bool {
	switch status {
	case "scheduled", "running", "stopped", "backup_complete", "binary_replaced", "rolling_back", "rollback_failed":
		return true
	default:
		return false
	}
}

func verifyCandidate(ctx context.Context, candidatePath, version, versionHash string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, candidatePath, "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("candidate version probe failed: %w", err)
	}
	var metadata candidateVersion
	if err := json.Unmarshal(output, &metadata); err != nil {
		return fmt.Errorf("candidate returned invalid version metadata: %w", err)
	}
	if metadata.Version != version || !strings.EqualFold(metadata.Hash, versionHash) {
		return errors.New("candidate version metadata does not match the release manifest")
	}
	return nil
}

func randomID() (string, error) {
	content := make([]byte, 6)
	if _, err := rand.Read(content); err != nil {
		return "", err
	}
	return hex.EncodeToString(content), nil
}

func atomicWriteJSON(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return err
	}
	return renameReplace(temporary, path)
}

func renameReplace(source, destination string) error {
	if runtime.GOOS == "windows" {
		_ = os.Remove(destination)
	}
	return os.Rename(source, destination)
}
