package selfupdate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

var errRollbackFailed = errors.New("self-update rollback failed")
var errUnsafeHealthURL = errors.New("unsafe self-update health check URL")

type transaction struct {
	config      HelperConfig
	systemctl   func(...string) error
	waitHealthy func(string, string, string, time.Duration, time.Duration) error
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
	if err := validateHelperConfig(&config); err != nil {
		return err
	}
	if config.StartDelay > 0 {
		time.Sleep(config.StartDelay)
	}
	tx := transaction{config: config, systemctl: runSystemctl, waitHealthy: waitForHealthy}
	err = tx.run()
	if err == nil {
		_ = os.RemoveAll(filepath.Dir(configPath))
		pruneRollbackSnapshots(filepath.Dir(config.BackupRoot), config.BackupRoot, 2)
	}
	return helperExitError(err)
}

func helperExitError(err error) error {
	if errors.Is(err, errRollbackFailed) {
		// rollback_failed is a recorded terminal state. Exit successfully so an
		// older launcher using Restart=on-failure cannot replay the rollback.
		return nil
	}
	return err
}

func validateHelperConfig(config *HelperConfig) error {
	if config == nil {
		return errors.New("missing helper configuration")
	}
	if config.JobID == "" || !versionPattern.MatchString(config.ExpectedVersion) || !hashPattern.MatchString(config.ExpectedHash) {
		return errors.New("invalid helper configuration")
	}
	if _, err := validateInitialHealthURL(config.HealthURL); err != nil {
		return err
	}
	if !pathWithin(config.DataDir, filepath.Dir(config.CurrentExecutable)) || filepath.Base(config.DataDir) != "data" {
		return errors.New("unsafe data directory in helper configuration")
	}
	if !pathWithin(config.CandidateExecutable, config.UpdateRoot) || !pathWithin(config.BackupRoot, filepath.Dir(config.CurrentExecutable)) {
		return errors.New("unsafe update path in helper configuration")
	}
	if config.HealthTimeout <= 0 {
		config.HealthTimeout = defaultHealthTimeout
	}
	if config.StableWindow <= 0 {
		config.StableWindow = defaultStableWindow
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
	if result.Status == "rollback_failed" {
		return errRollbackFailed
	}
	if result.Status == "rolling_back" {
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
	} else if err := tx.waitHealthy(tx.config.HealthURL, tx.config.ExpectedVersion, tx.config.ExpectedHash, tx.config.HealthTimeout, tx.config.StableWindow); err != nil {
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
		result.Message = rollbackFailureMessage(result.Message, err)
		result.UpdatedAt = time.Now().UTC()
		tx.writeResult(result)
		return fmt.Errorf("%w: %s", errRollbackFailed, result.Message)
	}
	result.Status = "rolled_back"
	result.CurrentVersion = tx.config.PreviousVersion
	result.CurrentHash = tx.config.PreviousHash
	result.UpdatedAt = time.Now().UTC()
	tx.writeResult(result)
	return nil
}

func rollbackFailureMessage(updateMessage string, rollbackErr error) string {
	updateMessage = strings.TrimSpace(updateMessage)
	if updateMessage == "" {
		return "rollback failed: " + rollbackErr.Error()
	}
	return updateMessage + "; rollback failed: " + rollbackErr.Error()
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
	if err := tx.waitHealthy(tx.config.HealthURL, tx.config.PreviousVersion, tx.config.PreviousHash, tx.config.HealthTimeout, 3*time.Second); err != nil {
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

func waitForHealthy(healthURL, version, versionHash string, timeout, stableWindow time.Duration) error {
	deadline := time.Now().Add(timeout)
	var stableSince time.Time
	client, err := newLoopbackHealthClient(healthURL)
	if err != nil {
		return err
	}
	for time.Now().Before(deadline) {
		matched, checkErr := checkHealthy(client, healthURL, version, versionHash)
		if errors.Is(checkErr, errUnsafeHealthURL) {
			return checkErr
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

func newLoopbackHealthClient(healthURL string) (*http.Client, error) {
	return newLoopbackHealthClientWithRoots(healthURL, nil)
}

func newLoopbackHealthClientWithRoots(healthURL string, roots *x509.CertPool) (*http.Client, error) {
	if _, err := validateInitialHealthURL(healthURL); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{
		// This client can only reach literal loopback addresses, enforced for
		// both the initial URL and every redirect below.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("loopback HTTPS server did not provide a certificate")
			}
			intermediates := x509.NewCertPool()
			for _, certificate := range state.PeerCertificates[1:] {
				intermediates.AddCert(certificate)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:         roots,
				Intermediates: intermediates,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   3 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many health check redirects")
			}
			parsed, err := validateLoopbackHealthURL(request.URL.String())
			if err != nil {
				return err
			}
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && parsed.Scheme != "https" {
				return errors.New("health check redirect must not downgrade HTTPS")
			}
			return nil
		},
	}, nil
}

func validateInitialHealthURL(rawURL string) (*url.URL, error) {
	parsed, err := validateHealthURL(rawURL)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(parsed.Hostname())
	if parsed.Scheme == "https" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("%w: HTTPS URL must use a literal loopback address", errUnsafeHealthURL)
	}
	return parsed, nil
}

func validateLoopbackHealthURL(rawURL string) (*url.URL, error) {
	parsed, err := validateHealthURL(rawURL)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("%w: URL must use a literal loopback address", errUnsafeHealthURL)
	}
	return parsed, nil
}

func validateHealthURL(rawURL string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid URL: %v", errUnsafeHealthURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: URL must use HTTP or HTTPS", errUnsafeHealthURL)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%w: URL must not contain user information", errUnsafeHealthURL)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("%w: URL must include a host", errUnsafeHealthURL)
	}
	if parsed.Port() == "" {
		return nil, fmt.Errorf("%w: URL must include a port", errUnsafeHealthURL)
	}
	if parsed.EscapedPath() != "/api/version" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: URL must target /api/version", errUnsafeHealthURL)
	}
	return parsed, nil
}

func checkHealthy(client *http.Client, healthURL, version, versionHash string) (bool, error) {
	resp, err := client.Get(healthURL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	var envelope struct {
		Data candidateVersion `json:"data"`
		candidateVersion
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false, err
	}
	observed := envelope.Data
	if observed.Version == "" {
		observed = envelope.candidateVersion
	}
	return observed.Version == version && observed.Hash == versionHash, nil
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
