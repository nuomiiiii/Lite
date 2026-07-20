package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
