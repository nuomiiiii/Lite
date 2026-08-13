package selfupdate

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testHealthVersion = "2.2.3"
	testHealthHash    = "abc1234"
)

func TestWaitForHealthyHTTPLoopback(t *testing.T) {
	server := newVersionServer(t, testHealthVersion, testHealthHash)
	defer server.Close()

	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForHealthyWithClient(client, healthURL, testHealthVersion, testHealthHash, 200*time.Millisecond, 10*time.Millisecond, 2*time.Millisecond); err != nil {
		t.Fatalf("HTTP loopback health check failed: %v", err)
	}
}

func TestWaitForHealthyRedirectsToLoopbackHTTPSWithoutHostnameSAN(t *testing.T) {
	certificate, roots := trustedCertificateWithoutLoopbackSAN(t)
	tlsServer := httptest.NewUnstartedServer(versionHandler(testHealthVersion, testHealthHash))
	tlsServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	tlsServer.StartTLS()
	defer tlsServer.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, tlsServer.URL+"/api/version", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	healthURL := redirect.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL, roots)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForHealthyWithClient(client, healthURL, testHealthVersion, testHealthHash, 300*time.Millisecond, 10*time.Millisecond, 2*time.Millisecond); err != nil {
		t.Fatalf("loopback HTTPS redirect health check failed: %v", err)
	}
}

func TestLoopbackHTTPSStillVerifiesCertificateChain(t *testing.T) {
	tlsServer := httptest.NewUnstartedServer(versionHandler(testHealthVersion, testHealthHash))
	tlsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsServer.StartTLS()
	defer tlsServer.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, tlsServer.URL+"/api/version", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	healthURL := redirect.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL, x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForHealthyWithClient(client, healthURL, testHealthVersion, testHealthHash, 40*time.Millisecond, time.Millisecond, 2*time.Millisecond); err == nil {
		t.Fatal("health check accepted an untrusted HTTPS certificate")
	}
}

func TestLoopbackHealthClientDoesNotChangeOtherHTTPClients(t *testing.T) {
	defaultTransport := http.DefaultTransport.(*http.Transport)
	defaultTLSConfig := defaultTransport.TLSClientConfig
	healthURL := "http://127.0.0.1:25774/api/version"
	client, err := newLoopbackHealthClient(healthURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.Transport == http.DefaultTransport {
		t.Fatal("health client reused the global HTTP transport")
	}
	if defaultTransport.TLSClientConfig != defaultTLSConfig {
		t.Fatal("health client changed the global TLS configuration")
	}
	downloadClient := updateHTTPClient()
	if downloadClient.Transport != nil {
		t.Fatal("ordinary update downloads unexpectedly use the health-check transport")
	}
}

func TestLoopbackHealthRedirectRejectsNonLoopbackTargets(t *testing.T) {
	tests := []string{
		"https://komari.example:443/api/version",
		"https://192.168.1.20:443/api/version",
		"https://8.8.8.8:443/api/version",
		"https://[2001:db8::1]:443/api/version",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, target, http.StatusTemporaryRedirect)
			}))
			defer redirect.Close()
			healthURL := redirect.URL + "/api/version"
			client, err := newLoopbackHealthClient(healthURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Get(healthURL)
			if err == nil {
				response.Body.Close()
				t.Fatalf("health check followed redirect to %s", target)
			}
		})
	}
}

func TestParseLoopbackHealthURLAllowsIPv4AndIPv6Only(t *testing.T) {
	for _, healthURL := range []string{
		"http://127.0.0.1:25774/api/version",
		"http://[::1]:25774/api/version",
	} {
		if _, err := parseLoopbackHealthURL(healthURL, "http"); err != nil {
			t.Fatalf("approved loopback URL %q was rejected: %v", healthURL, err)
		}
	}
	for _, healthURL := range []string{
		"http://localhost:25774/api/version",
		"http://127.0.0.2:25774/api/version",
		"http://[::ffff:127.0.0.1]:25774/api/version",
	} {
		if _, err := parseLoopbackHealthURL(healthURL, "http"); err == nil {
			t.Fatalf("unapproved health URL %q was accepted", healthURL)
		}
	}
}

func TestWaitForHealthyRequiresExactHash(t *testing.T) {
	server := newVersionServer(t, testHealthVersion, "ABC1234")
	defer server.Close()
	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForHealthyWithClient(client, healthURL, testHealthVersion, testHealthHash, 40*time.Millisecond, time.Millisecond, 2*time.Millisecond); err == nil {
		t.Fatal("health check accepted a case-mismatched build hash")
	}
}

func TestWaitForHealthyRequiresContinuousStableWindow(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hash := testHealthHash
		if requests.Add(1) == 2 {
			hash = "bad9999"
		}
		writeVersion(writer, testHealthVersion, hash)
	}))
	defer server.Close()
	healthURL := server.URL + "/api/version"
	client, err := newLoopbackHealthClient(healthURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	stableWindow := 20 * time.Millisecond
	started := time.Now()
	if err := waitForHealthyWithClient(client, healthURL, testHealthVersion, testHealthHash, 200*time.Millisecond, stableWindow, 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 5 || time.Since(started) < stableWindow+5*time.Millisecond {
		t.Fatalf("health check did not restart its stable window: requests=%d elapsed=%s", requests.Load(), time.Since(started))
	}
}

func TestRollbackFailedIsTerminalOnRestart(t *testing.T) {
	tx, root := newTestTransaction(t)
	result := UpdateResult{JobID: tx.config.JobID, Status: "rollback_failed", UpdatedAt: time.Now().UTC()}
	if err := atomicWriteJSON(filepath.Join(root, updateRootName, lastResultName), result, 0600); err != nil {
		t.Fatal(err)
	}
	commands := 0
	tx.systemctl = func(...string) error {
		commands++
		return nil
	}
	tx.waitHealthy = func(string, string, time.Duration, time.Duration) error {
		t.Fatal("terminal transaction performed another health check")
		return nil
	}
	if err := tx.run(); err != nil {
		t.Fatalf("terminal transaction returned an error: %v", err)
	}
	if commands != 0 {
		t.Fatalf("terminal transaction called systemctl %d times", commands)
	}
}

func TestRollbackFailedIsNotInProgress(t *testing.T) {
	if isUpdateInProgress("rollback_failed") {
		t.Fatal("rollback_failed was treated as an active update")
	}
}

func TestRollbackFailureMessageDoesNotGrowRecursively(t *testing.T) {
	for _, previous := range []string{
		"updated service health check: timeout; rollback failed: old failure; rollback failed: repeated failure",
		"rollback failed: old failure",
	} {
		message := rollbackFailureMessage(previous, errors.New("current failure"), nil)
		if strings.Count(strings.ToLower(message), "rollback failed:") != 1 {
			t.Fatalf("rollback failure was duplicated: %q", message)
		}
		if !strings.Contains(message, "current failure") {
			t.Fatalf("rollback failure lost its current cause: %q", message)
		}
	}
}

func TestRollbackFailureReturnsSuccessAndEnsuresServiceStart(t *testing.T) {
	tx, root := newTestTransaction(t)
	var commands []string
	tx.systemctl = func(arguments ...string) error {
		commands = append(commands, strings.Join(arguments, " "))
		return nil
	}
	result := UpdateResult{
		JobID:     tx.config.JobID,
		Status:    "rolling_back",
		Message:   "updated service health check: timeout",
		UpdatedAt: time.Now().UTC(),
	}
	if err := tx.finishRollback(result, filepath.Join(root, "missing-komari"), filepath.Join(root, "missing-data")); err != nil {
		t.Fatalf("rollback_failed must exit successfully for old Restart=on-failure units: %v", err)
	}
	if len(commands) == 0 || commands[len(commands)-1] != "start komari.service" {
		t.Fatalf("rollback failure did not finish by ensuring the service was started: %v", commands)
	}
	stored, err := ReadLastResult(root)
	if err != nil || stored == nil || stored.Status != "rollback_failed" {
		t.Fatalf("last result = %#v, err = %v", stored, err)
	}
}

func newVersionServer(t *testing.T, version, hash string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(versionHandler(version, hash))
}

func versionHandler(version, hash string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/version" {
			http.NotFound(writer, request)
			return
		}
		writeVersion(writer, version, hash)
	})
}

func writeVersion(writer http.ResponseWriter, version, hash string) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]string{"version": version, "hash": hash})
}

func trustedCertificateWithoutLoopbackSAN(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Komari test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "monitor.example"},
		DNSNames:     []string{"monitor.example"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, ca, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{serverDER, caDER}, PrivateKey: serverKey}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return certificate, roots
}
