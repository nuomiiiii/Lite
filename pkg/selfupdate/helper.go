package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type transaction struct {
	config      HelperConfig
	systemctl   func(...string) error
	waitHealthy func(string, string, time.Duration, time.Duration) error
}

func RunHelper(configPath string) error {
	if runtime.GOOS != "linux" {
		return errors.New("the self-update helper is only available on Linux")
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var config HelperConfig
	if err := json.Unmarshal(content, &config); err != nil {
		return err
	}
	if err := validateHelperConfig(config); err != nil {
		return err
	}
	helperHealthURL = config.HealthURL
	if config.StartDelay > 0 {
		time.Sleep(config.StartDelay)
	}
	tx := transaction{config: config, systemctl: runSystemctl, waitHealthy: waitForHealthy}
	err = tx.run()
	if err == nil {
		_ = os.RemoveAll(filepath.Dir(configPath))
		pruneRollbackSnapshots(filepath.Dir(config.BackupRoot), config.BackupRoot, 2)
	}
	return err
}

func validateHelperConfig(config HelperConfig) error {
	if config.JobID == "" || !versionPattern.MatchString(config.ExpectedVersion) || !hashPattern.MatchString(config.ExpectedHash) {
		return errors.New("invalid helper configuration")
	}
	if !pathWithin(config.DataDir, filepath.Dir(config.CurrentExecutable)) || filepath.Base(config.DataDir) != "data" {
		return errors.New("unsafe data directory in helper configuration")
	}
	if !pathWithin(config.CandidateExecutable, config.UpdateRoot) || !pathWithin(config.BackupRoot, filepath.Dir(config.CurrentExecutable)) {
		return errors.New("unsafe update path in helper configuration")
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = 90 * time.Second
	}
	if config.StableWindow <= 0 {
		config.StableWindow = 15 * time.Second
	}
	return nil
}

func (tx transaction) run() error {
	result := UpdateResult{
		JobID:         tx.config.JobID,
		Status:        "running",
		TargetVersion: tx.config.ExpectedVersion,
		TargetHash:    tx.config.ExpectedHash,
		UpdatedAt:     time.Now().UTC(),
	}
	backupData := filepath.Join(tx.config.BackupRoot, "data")
	backupExecutable := filepath.Join(tx.config.BackupRoot, "komari")

	if previous, err := ReadLastResult(filepath.Dir(tx.config.CurrentExecutable)); err == nil && previous != nil && previous.JobID == tx.config.JobID {
		result = *previous
	}
	if result.Status == "succeeded" || result.Status == "rolled_back" || result.Status == "failed" {
		return nil
	}
	if result.Status == "rolling_back" || result.Status == "rollback_failed" {
		return tx.finishRollback(result, backupExecutable, backupData)
	}

	if result.Status != "stopped" && result.Status != "backup_complete" && result.Status != "binary_replaced" {
		result.Status = "running"
		result.UpdatedAt = time.Now().UTC()
		tx.writeResult(result)
		if err := tx.systemctl("stop", tx.config.Service); err != nil {
			return tx.failWithoutSwap(result, fmt.Errorf("stop service: %w", err))
		}
		result.Status = "stopped"
		result.UpdatedAt = time.Now().UTC()
		tx.writeResult(result)
	}

	if result.Status == "stopped" {
		if err := os.MkdirAll(tx.config.BackupRoot, 0700); err != nil {
			_ = tx.systemctl("start", tx.config.Service)
			return tx.failWithoutSwap(result, fmt.Errorf("create rollback directory: %w", err))
		}
		if err := copyDirAtomic(tx.config.DataDir, backupData); err != nil {
			_ = tx.systemctl("start", tx.config.Service)
			return tx.failWithoutSwap(result, fmt.Errorf("create cold data snapshot: %w", err))
		}
		if err := copyFileAtomic(tx.config.CurrentExecutable, backupExecutable, 0755); err != nil {
			_ = tx.systemctl("start", tx.config.Service)
			return tx.failWithoutSwap(result, fmt.Errorf("backup current executable: %w", err))
		}
		result.Status = "backup_complete"
		result.UpdatedAt = time.Now().UTC()
		tx.writeResult(result)
	}

	if result.Status == "backup_complete" {
		if err := copyFileAtomic(tx.config.CandidateExecutable, tx.config.CurrentExecutable, 0755); err != nil {
			_ = tx.systemctl("start", tx.config.Service)
			return tx.failWithoutSwap(result, fmt.Errorf("replace executable: %w", err))
		}
		result.Status = "binary_replaced"
		result.UpdatedAt = time.Now().UTC()
		tx.writeResult(result)
	}

	var updateErr error
	if err := tx.systemctl("start", tx.config.Service); err != nil {
		updateErr = fmt.Errorf("start updated service: %w", err)
	} else if err := tx.waitHealthy(tx.config.ExpectedVersion, tx.config.ExpectedHash, tx.config.HealthTimeout, tx.config.StableWindow); err != nil {
		updateErr = fmt.Errorf("updated service health check: %w", err)
	} else {
		result.Status = "succeeded"
		result.CurrentVersion = tx.config.ExpectedVersion
		result.CurrentHash = tx.config.ExpectedHash
		result.Message = ""
		result.UpdatedAt = time.Now().UTC()
		tx.writeResult(result)
		return nil
	}

	result.Status = "rolling_back"
	result.Message = updateErr.Error()
	result.UpdatedAt = time.Now().UTC()
	tx.writeResult(result)
	return tx.finishRollback(result, backupExecutable, backupData)
}

func (tx transaction) finishRollback(result UpdateResult, backupExecutable, backupData string) error {
	if err := tx.rollback(backupExecutable, backupData); err != nil {
		result.Status = "rollback_failed"
		result.Message = strings.TrimSpace(result.Message + "; rollback failed: " + err.Error())
		result.UpdatedAt = time.Now().UTC()
		tx.writeResult(result)
		return errors.New(result.Message)
	}
	result.Status = "rolled_back"
	result.CurrentVersion = tx.config.PreviousVersion
	result.CurrentHash = tx.config.PreviousHash
	result.UpdatedAt = time.Now().UTC()
	tx.writeResult(result)
	return nil
}

func (tx transaction) rollback(backupExecutable, backupData string) error {
	_ = tx.systemctl("stop", tx.config.Service)
	if err := copyFileAtomic(backupExecutable, tx.config.CurrentExecutable, 0755); err != nil {
		return fmt.Errorf("restore executable: %w", err)
	}
	if _, err := os.Stat(backupData); err == nil {
		failedData := filepath.Join(tx.config.BackupRoot, "failed-data")
		if _, err := os.Stat(failedData); err == nil {
			failedData += "-" + time.Now().Format("150405")
		}
		if _, err := os.Stat(tx.config.DataDir); err == nil {
			if err := os.Rename(tx.config.DataDir, failedData); err != nil {
				return fmt.Errorf("preserve failed data: %w", err)
			}
		}
		if err := os.Rename(backupData, tx.config.DataDir); err != nil {
			_ = os.Rename(failedData, tx.config.DataDir)
			return fmt.Errorf("restore data snapshot: %w", err)
		}
	} else if _, dataErr := os.Stat(tx.config.DataDir); dataErr != nil {
		return errors.New("both the rollback snapshot and data directory are unavailable")
	}
	if err := tx.systemctl("start", tx.config.Service); err != nil {
		return fmt.Errorf("restart previous service: %w", err)
	}
	if err := tx.waitHealthy(tx.config.PreviousVersion, tx.config.PreviousHash, tx.config.HealthTimeout, 3*time.Second); err != nil {
		return fmt.Errorf("previous service health check: %w", err)
	}
	return nil
}

func (tx transaction) failWithoutSwap(result UpdateResult, err error) error {
	result.Status = "failed"
	result.Message = err.Error()
	result.UpdatedAt = time.Now().UTC()
	tx.writeResult(result)
	return nil
}

func (tx transaction) writeResult(result UpdateResult) {
	_ = atomicWriteJSON(filepath.Join(tx.config.UpdateRoot, lastResultName), result, 0600)
}

func runSystemctl(arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func waitForHealthy(version, versionHash string, timeout, stableWindow time.Duration) error {
	deadline := time.Now().Add(timeout)
	var stableSince time.Time
	client := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		matched := false
		resp, err := client.Get(currentHelperHealthURL())
		if err == nil {
			var envelope struct {
				Data candidateVersion `json:"data"`
				candidateVersion
			}
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK && json.Unmarshal(body, &envelope) == nil {
				observed := envelope.Data
				if observed.Version == "" {
					observed = envelope.candidateVersion
				}
				matched = observed.Version == version && strings.EqualFold(observed.Hash, versionHash)
			}
		}
		if matched {
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= stableWindow {
				return nil
			}
		} else {
			stableSince = time.Time{}
		}
		time.Sleep(time.Second)
	}
	return errors.New("health check timed out")
}

var helperHealthURL string

func currentHelperHealthURL() string {
	return helperHealthURL
}

func copyDirAtomic(source, destination string) error {
	temporary := destination + ".tmp"
	_ = os.RemoveAll(temporary)
	if err := copyDir(source, temporary); err != nil {
		_ = os.RemoveAll(temporary)
		return err
	}
	_ = os.RemoveAll(destination)
	return renameReplace(temporary, destination)
}

func copyDir(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("snapshot source is not a directory")
	}
	if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
		return err
	}
	return filepath.Walk(source, func(path string, entry os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, entry.Mode().Perm())
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("unsupported file in data directory: %s", path)
		}
		return copyFile(path, target, entry.Mode().Perm())
	})
}

func copyFileAtomic(source, destination string, mode os.FileMode) error {
	temporary := destination + ".tmp"
	if err := copyFile(source, temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, destination)
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func pruneRollbackSnapshots(parent, keep string, limit int) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	type snapshot struct {
		path    string
		modTime time.Time
	}
	var snapshots []snapshot
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "self-update-") {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		if filepath.Clean(path) == filepath.Clean(keep) {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			snapshots = append(snapshots, snapshot{path: path, modTime: info.ModTime()})
		}
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].modTime.After(snapshots[j].modTime) })
	for index := limit - 1; index < len(snapshots); index++ {
		if index >= 0 {
			_ = os.RemoveAll(snapshots[index].path)
		}
	}
}
