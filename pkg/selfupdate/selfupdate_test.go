package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestScheduleUpdateHelperFallsBackWithoutNoBlock(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name != "systemd-run" {
			t.Fatalf("command = %q, want systemd-run", name)
		}
		calls = append(calls, append([]string(nil), arguments...))
		if len(calls) == 1 {
			return []byte("systemd-run: unrecognized option '--no-block'"), errors.New("exit status 1")
		}
		return []byte("Running as unit: komari-self-update-test.service"), nil
	}

	if output, err := scheduleUpdateHelper(context.Background(), "test", "/tmp/candidate", "/tmp/helper.json", run); err != nil {
		t.Fatalf("scheduleUpdateHelper() output = %q, error = %v", output, err)
	}
	if len(calls) != 2 {
		t.Fatalf("systemd-run calls = %d, want 2", len(calls))
	}
	if !containsArgument(calls[0], "--no-block") {
		t.Fatal("first systemd-run call did not use --no-block")
	}
	if containsArgument(calls[1], "--no-block") {
		t.Fatal("compatible systemd-run retry still used --no-block")
	}
}

func TestScheduleUpdateHelperFallsBackForCentOS7RestartProperties(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name != "systemd-run" {
			t.Fatalf("command = %q, want systemd-run", name)
		}
		calls = append(calls, append([]string(nil), arguments...))
		if len(calls) == 1 {
			return []byte("Unknown assignment Restart=on-failure. Failed to create bus message: No such device or address"), errors.New("exit status 1")
		}
		return []byte("Running as unit: komari-self-update-test.service"), nil
	}

	if output, err := scheduleUpdateHelper(context.Background(), "test", "/tmp/candidate", "/tmp/helper.json", run); err != nil {
		t.Fatalf("scheduleUpdateHelper() output = %q, error = %v", output, err)
	}
	if len(calls) != 2 {
		t.Fatalf("systemd-run calls = %d, want 2", len(calls))
	}
	if !containsArgument(calls[1], "--no-block") {
		t.Fatal("CentOS 7 compatibility retry unexpectedly removed --no-block")
	}
	if containsArgument(calls[1], "--property=Restart=on-failure") || containsArgument(calls[1], "--property=RestartSec=3s") {
		t.Fatal("CentOS 7 compatibility retry still used unsupported restart properties")
	}
}

func TestScheduleUpdateHelperCombinesLegacyFallbacks(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), arguments...))
		switch len(calls) {
		case 1:
			return []byte("systemd-run: unrecognized option '--no-block'"), errors.New("exit status 1")
		case 2:
			return []byte("Unknown assignment Restart=on-failure"), errors.New("exit status 1")
		default:
			return []byte("Running as unit: komari-self-update-test.service"), nil
		}
	}

	if output, err := scheduleUpdateHelper(context.Background(), "test", "/tmp/candidate", "/tmp/helper.json", run); err != nil {
		t.Fatalf("scheduleUpdateHelper() output = %q, error = %v", output, err)
	}
	if len(calls) != 3 {
		t.Fatalf("systemd-run calls = %d, want 3", len(calls))
	}
	if containsArgument(calls[2], "--no-block") ||
		containsArgument(calls[2], "--property=Restart=on-failure") ||
		containsArgument(calls[2], "--property=RestartSec=3s") {
		t.Fatalf("final compatibility retry retained unsupported options: %v", calls[2])
	}
}

func TestScheduleUpdateHelperDoesNotRetryOtherFailures(t *testing.T) {
	calls := 0
	run := func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		calls++
		return []byte("Failed to start transient service unit"), errors.New("exit status 1")
	}

	if _, err := scheduleUpdateHelper(context.Background(), "test", "/tmp/candidate", "/tmp/helper.json", run); err == nil {
		t.Fatal("scheduleUpdateHelper() unexpectedly succeeded")
	}
	if calls != 1 {
		t.Fatalf("systemd-run calls = %d, want 1", calls)
	}
}

func containsArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

func TestDeploymentTypeHonorsExplicitMarker(t *testing.T) {
	t.Setenv("KOMARI_DEPLOYMENT", "docker")
	if got := DeploymentType(); got != DeploymentDocker {
		t.Fatalf("DeploymentType() = %q, want %q", got, DeploymentDocker)
	}
}

func TestManifestSelectsCurrentPlatformAndValidatesChecksum(t *testing.T) {
	assetName := "komari-" + runtime.GOOS + "-" + runtime.GOARCH
	manifest := Manifest{
		Schema:      1,
		Version:     "2.0.5",
		VersionHash: "ab12cd3",
		Assets: []ManifestAsset{{
			Name:   assetName,
			OS:     runtime.GOOS,
			Arch:   runtime.GOARCH,
			Size:   42,
			SHA256: strings.Repeat("a", 64),
		}},
	}
	asset, err := manifest.validate("2.0.5", "AB12CD3")
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if asset.Name != assetName {
		t.Fatalf("asset name = %q, want %q", asset.Name, assetName)
	}
}

func TestTransactionKeepsSnapshotAfterSuccessfulUpdate(t *testing.T) {
	tx, root := newTestTransaction(t)
	tx.waitHealthy = func(version, hash string, _, _ time.Duration) error {
		if version != "2.0.5" || hash != "new1234" {
			t.Fatalf("unexpected health target %s (%s)", version, hash)
		}
		return nil
	}
	if err := tx.run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	assertFileContent(t, tx.config.CurrentExecutable, "new-binary")
	assertFileContent(t, filepath.Join(tx.config.DataDir, "state"), "before")
	assertFileContent(t, filepath.Join(tx.config.BackupRoot, "komari"), "old-binary")
	assertFileContent(t, filepath.Join(tx.config.BackupRoot, "data", "state"), "before")
	result, err := ReadLastResult(root)
	if err != nil || result == nil || result.Status != "succeeded" {
		t.Fatalf("last result = %#v, err = %v", result, err)
	}
}

func TestTransactionRestoresBinaryAndDataAfterFailedHealthCheck(t *testing.T) {
	tx, root := newTestTransaction(t)
	tx.waitHealthy = func(version, _ string, _, _ time.Duration) error {
		if version == "2.0.5" {
			if err := os.WriteFile(filepath.Join(tx.config.DataDir, "state"), []byte("migrated"), 0600); err != nil {
				t.Fatal(err)
			}
			return errors.New("candidate crashed")
		}
		return nil
	}
	if err := tx.run(); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	assertFileContent(t, tx.config.CurrentExecutable, "old-binary")
	assertFileContent(t, filepath.Join(tx.config.DataDir, "state"), "before")
	assertFileContent(t, filepath.Join(tx.config.BackupRoot, "failed-data", "state"), "migrated")
	result, err := ReadLastResult(root)
	if err != nil || result == nil || result.Status != "rolled_back" {
		t.Fatalf("last result = %#v, err = %v", result, err)
	}
}

func newTestTransaction(t *testing.T) (transaction, string) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, "komari")
	candidate := filepath.Join(root, updateRootName, "jobs", "test", "candidate")
	dataDir := filepath.Join(root, "data")
	for path, content := range map[string]string{
		current:                         "old-binary",
		candidate:                       "new-binary",
		filepath.Join(dataDir, "state"): "before",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := HelperConfig{
		JobID:               "test-job",
		CurrentExecutable:   current,
		CandidateExecutable: candidate,
		DataDir:             dataDir,
		Service:             "komari.service",
		ExpectedVersion:     "2.0.5",
		ExpectedHash:        "new1234",
		PreviousVersion:     "2.0.4",
		PreviousHash:        "old1234",
		UpdateRoot:          filepath.Join(root, updateRootName),
		BackupRoot:          filepath.Join(root, "backup", "self-update-test"),
		HealthTimeout:       time.Second,
		StableWindow:        time.Millisecond,
	}
	tx := transaction{
		config: config,
		systemctl: func(...string) error {
			return nil
		},
	}
	return tx, root
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("content of %s = %q, want %q", path, content, want)
	}
}
