package selfupdate

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestScheduleUpdateHelperDoesNotEnableSystemdRestart(t *testing.T) {
	var arguments []string
	run := func(_ context.Context, _ string, received ...string) ([]byte, error) {
		arguments = append([]string(nil), received...)
		return []byte("Running as unit: komari-self-update-test.service"), nil
	}
	if _, err := scheduleUpdateHelper(context.Background(), "test", "/tmp/candidate", "/tmp/helper.json", run); err != nil {
		t.Fatal(err)
	}
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "--property=Restart") {
			t.Fatalf("systemd helper retained automatic restart property: %v", arguments)
		}
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

func TestLoopbackHealthCheckSupportsHTTP(t *testing.T) {
	server := httptest.NewServer(versionHandler("2.0.5", "new1234"))
	defer server.Close()
	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := checkHealthy(client, healthURL, "2.0.5", "new1234")
	if err != nil || !matched {
		t.Fatalf("HTTP health check matched = %v, error = %v", matched, err)
	}
}

func TestLoopbackHealthCheckSupportsIPv6WhenAvailable(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(versionHandler("2.0.5", "new1234"))
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := checkHealthy(client, healthURL, "2.0.5", "new1234")
	if err != nil || !matched {
		t.Fatalf("IPv6 health check matched = %v, error = %v", matched, err)
	}
}

func TestWaitForHealthyRequiresContinuousStableWindow(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		versionHandler("2.0.5", "new1234").ServeHTTP(writer, request)
	}))
	defer server.Close()
	if err := waitForHealthy(server.URL+"/api/version", "2.0.5", "new1234", 3*time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if requests < 2 {
		t.Fatalf("stable health window used only %d request", requests)
	}
}

func TestLoopbackHealthCheckFollowsHTTPSRedirectWithoutIPAddressSAN(t *testing.T) {
	tlsServer, roots := newDomainCertificateServer(t, versionHandler("2.0.5", "new1234"))
	defer tlsServer.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, tlsServer.URL+"/api/version", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	healthURL := redirect.URL + "/api/version"
	client, err := newLoopbackHealthClientWithRoots(healthURL, roots)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := checkHealthy(client, healthURL, "2.0.5", "new1234")
	if err != nil || !matched {
		t.Fatalf("redirected HTTPS health check matched = %v, error = %v", matched, err)
	}
}

func TestLoopbackHealthCheckStillRejectsUntrustedCertificate(t *testing.T) {
	tlsServer, _ := newDomainCertificateServer(t, versionHandler("2.0.5", "new1234"))
	defer tlsServer.Close()
	healthURL := tlsServer.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkHealthy(client, healthURL, "2.0.5", "new1234"); err == nil {
		t.Fatal("loopback health check accepted an untrusted certificate chain")
	}
}

func TestLoopbackHealthCheckRejectsExternalRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "https://example.com/api/version", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checkHealthy(client, healthURL, "2.0.5", "new1234"); !errors.Is(err, errUnsafeHealthURL) {
		t.Fatalf("external redirect error = %v, want unsafe health URL", err)
	}
}

func TestLoopbackHealthCheckRequiresVersionAndHash(t *testing.T) {
	server := httptest.NewServer(versionHandler("2.0.5", "wrong12"))
	defer server.Close()
	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := checkHealthy(client, healthURL, "2.0.5", "new1234")
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("health check accepted a mismatched seven-character build identifier")
	}
}

func TestLoopbackHealthCheckRequiresExactHashCase(t *testing.T) {
	server := httptest.NewServer(versionHandler("2.0.5", "NEW1234"))
	defer server.Close()
	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := checkHealthy(client, healthURL, "2.0.5", "new1234")
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("health check accepted a build identifier with different case")
	}
}

func TestLoopbackHealthURLAcceptsIPv4AndIPv6Only(t *testing.T) {
	for _, healthURL := range []string{
		"http://127.0.0.1:25774/api/version",
		"https://[::1]:18328/api/version",
	} {
		if _, err := validateLoopbackHealthURL(healthURL); err != nil {
			t.Errorf("validateLoopbackHealthURL(%q) error = %v", healthURL, err)
		}
	}
	for _, healthURL := range []string{
		"https://example.com:18328/api/version",
		"https://192.0.2.1:18328/api/version",
		"https://localhost:18328/api/version",
		"https://127.0.0.1:18328/other",
	} {
		if _, err := validateLoopbackHealthURL(healthURL); !errors.Is(err, errUnsafeHealthURL) {
			t.Errorf("validateLoopbackHealthURL(%q) error = %v, want unsafe health URL", healthURL, err)
		}
	}
}

func TestInitialHealthURLKeepsSpecificHTTPListenersButRestrictsHTTPS(t *testing.T) {
	if _, err := validateInitialHealthURL("http://192.0.2.10:25774/api/version"); err != nil {
		t.Fatalf("specific HTTP listener was rejected: %v", err)
	}
	if _, err := validateInitialHealthURL("http://localhost:25774/api/version"); err != nil {
		t.Fatalf("legacy HTTP hostname listener was rejected: %v", err)
	}
	if _, err := validateInitialHealthURL("https://192.0.2.10:18328/api/version"); !errors.Is(err, errUnsafeHealthURL) {
		t.Fatalf("non-loopback HTTPS error = %v, want unsafe health URL", err)
	}
}

func TestLocalHealthURLUsesLoopbackForWildcardListeners(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })

	for _, test := range []struct {
		listen string
		want   string
	}{
		{listen: "0.0.0.0:25774", want: "http://127.0.0.1:25774/api/version"},
		{listen: "[::]:25774", want: "http://[::1]:25774/api/version"},
	} {
		os.Args = []string{"komari", "--listen=" + test.listen}
		got, err := localHealthURL()
		if err != nil {
			t.Errorf("localHealthURL(%q) error = %v", test.listen, err)
			continue
		}
		if got != test.want {
			t.Errorf("localHealthURL(%q) = %q, want %q", test.listen, got, test.want)
		}
	}
}

func TestLocalHealthURLKeepsNonLoopbackSpecificHTTPListener(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"komari", "--listen=192.0.2.10:25774"}
	got, err := localHealthURL()
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://192.0.2.10:25774/api/version" {
		t.Fatalf("localHealthURL() = %q", got)
	}
}

func TestRollbackFailedIsTerminalAndDoesNotGrowMessage(t *testing.T) {
	tx, root := newTestTransaction(t)
	result := UpdateResult{
		JobID:         tx.config.JobID,
		Status:        "rollback_failed",
		TargetVersion: tx.config.ExpectedVersion,
		TargetHash:    tx.config.ExpectedHash,
		Message:       "updated service health check: timed out; rollback failed: previous service health check: timed out",
		UpdatedAt:     time.Now().UTC(),
	}
	if err := atomicWriteJSON(filepath.Join(tx.config.UpdateRoot, lastResultName), result, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(tx.config.UpdateRoot, lastResultName))
	if err != nil {
		t.Fatal(err)
	}
	serviceCalls := 0
	tx.systemctl = func(...string) error {
		serviceCalls++
		return nil
	}
	tx.waitHealthy = func(string, string, string, time.Duration, time.Duration) error {
		t.Fatal("terminal rollback unexpectedly performed a health check")
		return nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := tx.run(); !errors.Is(err, errRollbackFailed) {
			t.Fatalf("run() error = %v, want rollback_failed sentinel", err)
		}
	}
	if serviceCalls != 0 {
		t.Fatalf("terminal rollback invoked systemctl %d times", serviceCalls)
	}
	after, err := os.ReadFile(filepath.Join(root, updateRootName, lastResultName))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("terminal rollback rewrote or recursively expanded its result")
	}
	if err := helperExitError(errRollbackFailed); err != nil {
		t.Fatalf("rollback_failed must exit successfully under an old Restart=on-failure unit: %v", err)
	}
	if isUpdateInProgress("rollback_failed") {
		t.Fatal("rollback_failed is still reported as an in-progress transaction")
	}
}

func TestRollbackHealthFailureRunsOnceAndRemainsTerminal(t *testing.T) {
	tx, root := newTestTransaction(t)
	serviceCalls := 0
	tx.systemctl = func(...string) error {
		serviceCalls++
		return nil
	}
	healthCalls := 0
	tx.waitHealthy = func(_ string, version, _ string, _ time.Duration, _ time.Duration) error {
		healthCalls++
		if version == tx.config.ExpectedVersion {
			if err := os.WriteFile(filepath.Join(tx.config.DataDir, "state"), []byte("migrated"), 0600); err != nil {
				t.Fatal(err)
			}
			return errors.New("candidate unavailable")
		}
		return errors.New("previous service unavailable")
	}

	if err := tx.run(); !errors.Is(err, errRollbackFailed) {
		t.Fatalf("first run error = %v, want rollback_failed sentinel", err)
	}
	firstServiceCalls := serviceCalls
	firstHealthCalls := healthCalls
	firstResult, err := os.ReadFile(filepath.Join(root, updateRootName, lastResultName))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(firstResult), "rollback failed:"); got != 1 {
		t.Fatalf("rollback failure count = %d, want 1: %s", got, firstResult)
	}

	if err := tx.run(); !errors.Is(err, errRollbackFailed) {
		t.Fatalf("restarted run error = %v, want rollback_failed sentinel", err)
	}
	if serviceCalls != firstServiceCalls || healthCalls != firstHealthCalls {
		t.Fatalf("terminal restart repeated work: systemctl %d -> %d, health %d -> %d", firstServiceCalls, serviceCalls, firstHealthCalls, healthCalls)
	}
	secondResult, err := os.ReadFile(filepath.Join(root, updateRootName, lastResultName))
	if err != nil {
		t.Fatal(err)
	}
	if string(secondResult) != string(firstResult) {
		t.Fatal("terminal restart changed rollback_failed result")
	}
	assertFileContent(t, tx.config.CurrentExecutable, "old-binary")
	assertFileContent(t, filepath.Join(tx.config.DataDir, "state"), "before")
}

func TestTransactionKeepsSnapshotAfterSuccessfulUpdate(t *testing.T) {
	tx, root := newTestTransaction(t)
	tx.waitHealthy = func(_ string, version, hash string, _, _ time.Duration) error {
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
	tx.waitHealthy = func(_ string, version, _ string, _, _ time.Duration) error {
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
		HealthURL:           "http://127.0.0.1:25774/api/version",
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

func versionHandler(version, hash string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/version" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"version":%q,"hash":%q}`, version, hash)
	})
}

func newDomainCertificateServer(t *testing.T, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "komari.example"},
		DNSNames:              []string{"komari.example"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	tlsCertificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: privateKey, Leaf: certificate}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	_ = server.Listener.Close()
	server.Listener = listener
	server.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCertificate}}
	server.StartTLS()
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	parsed, err := url.Parse(server.URL)
	if err != nil || parsed.Hostname() != "127.0.0.1" {
		server.Close()
		t.Fatalf("unexpected TLS test server URL %q: %v", server.URL, err)
	}
	if err := certificate.VerifyHostname(parsed.Hostname()); err == nil {
		server.Close()
		t.Fatal("test certificate unexpectedly contains the loopback IP")
	}
	return server, roots
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
