package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
		return []byte("Running as unit: lite-self-update-test.service"), nil
	}

	if output, err := scheduleUpdateHelper(context.Background(), "test", "/tmp/candidate", "/tmp/helper.json", run); err != nil {
		t.Fatalf("scheduleUpdateHelper() output = %q, error = %v", output, err)
	}
	if len(calls) != 2 {
		t.Fatalf("systemd-run calls = %d, want 2", len(calls))
	}
	if !containsArgument(calls[0], "--unit=lite-self-update-test") {
		t.Fatal("first systemd-run call did not use the Lite helper unit prefix")
	}
	if !containsArgument(calls[0], "--no-block") {
		t.Fatal("first systemd-run call did not use --no-block")
	}
	for _, call := range calls {
		if containsArgument(call, "--property=Restart=on-failure") || containsArgument(call, "--property=RestartSec=3s") {
			t.Fatalf("systemd-run call enabled an unbounded helper restart: %v", call)
		}
	}
	if containsArgument(calls[1], "--no-block") {
		t.Fatal("compatible systemd-run retry still used --no-block")
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
	t.Setenv("LITE_DEPLOYMENT", "docker")
	if got := DeploymentType(); got != DeploymentDocker {
		t.Fatalf("DeploymentType() = %q, want %q", got, DeploymentDocker)
	}
}

func TestDeploymentTypeFallsBackToKomariEnv(t *testing.T) {
	t.Setenv("LITE_DEPLOYMENT", "")
	t.Setenv("KOMARI_DEPLOYMENT", "docker")
	if got := DeploymentType(); got != DeploymentDocker {
		t.Fatalf("DeploymentType() = %q, want %q", got, DeploymentDocker)
	}
}

func TestServiceNamePrefersLiteEnvThenKomariUnit(t *testing.T) {
	t.Setenv("LITE_SERVICE_NAME", "lite-custom")
	t.Setenv("KOMARI_SERVICE_NAME", "komari")
	if got := serviceName(); got != "lite-custom.service" {
		t.Fatalf("serviceName() = %q, want lite-custom.service", got)
	}

	t.Setenv("LITE_SERVICE_NAME", "")
	if got := serviceName(); got != "komari.service" {
		t.Fatalf("serviceName() = %q, want komari.service", got)
	}

	t.Setenv("KOMARI_SERVICE_NAME", "")
	previous := lookupServicePID
	t.Cleanup(func() { lookupServicePID = previous })
	pid := strconv.Itoa(os.Getpid())
	lookupServicePID = func(name string) string {
		if name == "komari.service" {
			return pid
		}
		return "0"
	}
	if got := serviceName(); got != "komari.service" {
		t.Fatalf("detected serviceName() = %q, want komari.service", got)
	}
}

func TestLocalHealthURLFallsBackToKomariListen(t *testing.T) {
	t.Setenv("LITE_LISTEN", "")
	t.Setenv("KOMARI_LISTEN", "0.0.0.0:25774")
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"Lite", "server"}
	got, err := localHealthURL()
	if err != nil {
		t.Fatalf("localHealthURL() error = %v", err)
	}
	if got != "http://127.0.0.1:25774/api/version" {
		t.Fatalf("localHealthURL() = %q, want Komari listen fallback", got)
	}

	t.Setenv("LITE_LISTEN", "127.0.0.1:27777")
	got, err = localHealthURL()
	if err != nil {
		t.Fatalf("localHealthURL() error = %v", err)
	}
	if got != "http://127.0.0.1:27777/api/version" {
		t.Fatalf("localHealthURL() = %q, want LITE_LISTEN to win", got)
	}
}

func TestConfiguredDatabaseWithinAcceptsKomariDB(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"Lite", "-d", "./data/komari.db"}
	if !configuredDatabaseWithin(dataDir, root) {
		t.Fatal("komari.db under data/ should stay a managed database path")
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
	asset, err := manifest.validate("2.0.5", "ab12cd3")
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if asset.Name != assetName {
		t.Fatalf("asset name = %q, want %q", asset.Name, assetName)
	}
	if _, err := manifest.validate("2.0.5", "AB12CD3"); err == nil {
		t.Fatal("manifest accepted a case-mismatched build identifier")
	}
}

func TestManifestPrefersLiteAssetName(t *testing.T) {
	liteName := "Lite-" + runtime.GOOS + "-" + runtime.GOARCH
	komariName := "komari-" + runtime.GOOS + "-" + runtime.GOARCH
	manifest := Manifest{
		Schema:      1,
		Version:     "2.2.3",
		VersionHash: "c0ffeee",
		Assets: []ManifestAsset{
			{
				Name:   komariName,
				OS:     runtime.GOOS,
				Arch:   runtime.GOARCH,
				Size:   11,
				SHA256: strings.Repeat("b", 64),
			},
			{
				Name:   liteName,
				OS:     runtime.GOOS,
				Arch:   runtime.GOARCH,
				Size:   42,
				SHA256: strings.Repeat("a", 64),
			},
		},
	}
	asset, err := manifest.validate("2.2.3", "c0ffeee")
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if asset.Name != liteName {
		t.Fatalf("asset name = %q, want %q", asset.Name, liteName)
	}
}

func TestMigrationHealthWindowCoversLowEndSQLiteUpgrade(t *testing.T) {
	if defaultHealthTimeout < 15*time.Minute {
		t.Fatalf("health timeout %s is too short for a low-end SQLite migration", defaultHealthTimeout)
	}
	if activeTransactionTimeout <= defaultHealthTimeout {
		t.Fatalf("active transaction timeout %s must outlive health timeout %s", activeTransactionTimeout, defaultHealthTimeout)
	}
}

func TestValidateHelperConfigAppliesMigrationDefaults(t *testing.T) {
	tx, _ := newTestTransaction(t)
	config := tx.config
	config.HealthTimeout = 0
	config.StableWindow = 0

	if err := validateHelperConfig(&config); err != nil {
		t.Fatalf("validateHelperConfig() error = %v", err)
	}
	if config.HealthTimeout != defaultHealthTimeout {
		t.Fatalf("health timeout = %s, want %s", config.HealthTimeout, defaultHealthTimeout)
	}
	if config.StableWindow != defaultStableWindow {
		t.Fatalf("stable window = %s, want %s", config.StableWindow, defaultStableWindow)
	}
}

func TestTransactionKeepsSnapshotAfterSuccessfulUpdate(t *testing.T) {
	tx, root := newTestTransaction(t)
	var commands []string
	tx.systemctl = func(arguments ...string) error {
		commands = append(commands, strings.Join(arguments, " "))
		return nil
	}
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
	assertFileContent(t, filepath.Join(tx.config.BackupRoot, "Lite"), "old-binary")
	assertFileContent(t, filepath.Join(tx.config.BackupRoot, "data", "state"), "before")
	result, err := ReadLastResult(root)
	if err != nil || result == nil || result.Status != "succeeded" {
		t.Fatalf("last result = %#v, err = %v", result, err)
	}
	if len(commands) == 0 || commands[len(commands)-1] != "start lite.service" {
		t.Fatalf("successful update did not leave the service started: %v", commands)
	}
}

func TestTransactionRestoresBinaryAndDataAfterFailedHealthCheck(t *testing.T) {
	tx, root := newTestTransaction(t)
	var commands []string
	tx.systemctl = func(arguments ...string) error {
		commands = append(commands, strings.Join(arguments, " "))
		return nil
	}
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
	if len(commands) == 0 || commands[len(commands)-1] != "start lite.service" {
		t.Fatalf("successful rollback did not leave the service started: %v", commands)
	}
}

func newTestTransaction(t *testing.T) (transaction, string) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, "Lite")
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
		Service:             "lite.service",
		ExpectedVersion:     "2.0.5",
		ExpectedHash:        "new1234",
		PreviousVersion:     "2.0.4",
		PreviousHash:        "old1234",
		HealthURL:           "http://127.0.0.1:27777/api/version",
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

func TestFetchReleaseManifestPrefersLiteThenKomari(t *testing.T) {
	previous := releaseBaseURL
	t.Cleanup(func() { releaseBaseURL = previous })

	liteBody, err := json.Marshal(Manifest{Schema: 1, Version: "2.2.5", VersionHash: "abc1234"})
	if err != nil {
		t.Fatal(err)
	}
	legacyBody, err := json.Marshal(Manifest{Schema: 1, Version: "2.2.4", VersionHash: "def5678"})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/"+manifestName):
			writer.Write(liteBody)
		case strings.HasSuffix(request.URL.Path, "/"+legacyManifestName):
			writer.Write(legacyBody)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	releaseBaseURL = server.URL

	got, err := fetchReleaseManifest("2.2.5", server.Client())
	if err != nil {
		t.Fatalf("fetchReleaseManifest() error = %v", err)
	}
	if got.Version != "2.2.5" || got.VersionHash != "abc1234" {
		t.Fatalf("preferred manifest = %#v", got)
	}
}

func TestFetchReleaseManifestFallsBackToKomariFile(t *testing.T) {
	previous := releaseBaseURL
	t.Cleanup(func() { releaseBaseURL = previous })

	legacyBody, err := json.Marshal(Manifest{Schema: 1, Version: "2.2.3", VersionHash: "oldhash"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/"+legacyManifestName) {
			writer.Write(legacyBody)
			return
		}
		http.NotFound(writer, request)
	}))
	t.Cleanup(server.Close)
	releaseBaseURL = server.URL

	got, err := fetchReleaseManifest("2.2.3", server.Client())
	if err != nil {
		t.Fatalf("fetchReleaseManifest() error = %v", err)
	}
	if got.Version != "2.2.3" || got.VersionHash != "oldhash" {
		t.Fatalf("legacy manifest = %#v", got)
	}
}

func TestReadLastResultFallsBackToLegacyUpdateRoot(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, legacyUpdateRootName)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	result := UpdateResult{
		JobID:         "legacy-job",
		Status:        "succeeded",
		TargetVersion: "2.2.3",
		UpdatedAt:     time.Now().UTC(),
	}
	content, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, lastResultName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLastResult(root)
	if err != nil || got == nil {
		t.Fatalf("ReadLastResult() = %#v, err = %v", got, err)
	}
	if got.JobID != "legacy-job" || got.Status != "succeeded" {
		t.Fatalf("legacy last result = %#v", got)
	}
}
