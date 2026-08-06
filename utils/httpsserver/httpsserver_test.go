package httpsserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerServesHTTPSWithManualCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestPair(t, dir, "localhost")
	manager := NewManager()
	settings := Settings{
		Enabled:         true,
		Listen:          ":0",
		CertificatePath: certPath,
		PrivateKeyPath:  keyPath,
	}
	if err := manager.Start(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}), settings); err != nil {
		t.Fatalf("start HTTPS manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Shutdown(context.Background())
	})

	manager.mu.RLock()
	address := manager.listener.Addr().String()
	manager.mu.RUnlock()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test-only self-signed certificate
	}}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split HTTPS listener address: %v", err)
	}
	wantIPv6 := false
	for _, host := range []string{"127.0.0.1", "[::1]"} {
		if host == "[::1]" {
			probe, probeErr := net.Listen("tcp6", "[::1]:0")
			if probeErr != nil {
				t.Logf("IPv6 loopback unavailable; skipping IPv6 handshake: %v", probeErr)
				continue
			}
			_ = probe.Close()
			wantIPv6 = true
		}
		response, requestErr := client.Get("https://" + host + ":" + port + "/status")
		if requestErr != nil {
			t.Fatalf("GET built-in HTTPS through %s: %v", host, requestErr)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if string(body) != "secure" {
			t.Fatalf("body through %s = %q", host, body)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	status := manager.Status()
	for !status.ListenerProbeDone && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		status = manager.Status()
	}
	if !status.Running || !status.Ready || len(status.Domains) == 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if !status.ListenerProbeDone || !status.ListenerIPv4 || (wantIPv6 && !status.ListenerIPv6) {
		t.Fatalf("listener family status does not match successful probes: %+v", status)
	}
}

func TestHTTPSPortRedirectsPlainHTTPToSamePort(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestPair(t, dir, "localhost")
	manager := NewManager()
	settings := Settings{
		Enabled:         true,
		Listen:          "127.0.0.1:0",
		RedirectHTTP:    true,
		CertificatePath: certPath,
		PrivateKeyPath:  keyPath,
	}
	if err := manager.Start(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}), settings); err != nil {
		t.Fatalf("start HTTPS manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.mu.RLock()
	address := manager.listener.Addr().String()
	manager.mu.RUnlock()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Get("http://" + address + "/admin?q=1")
	if err != nil {
		t.Fatalf("plain HTTP request to HTTPS port: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("plain HTTP status = %d", response.StatusCode)
	}
	if cacheControl := response.Header.Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("plain HTTP redirect cache control = %q", cacheControl)
	}
	if location := response.Header.Get("Location"); location != "https://"+address+"/admin?q=1" {
		t.Fatalf("plain HTTP redirect = %q", location)
	}

	secureClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test-only self-signed certificate
	}}
	secureResponse, err := secureClient.Get("https://" + address + "/status")
	if err != nil {
		t.Fatalf("HTTPS request after plain redirect: %v", err)
	}
	secureBody, _ := io.ReadAll(secureResponse.Body)
	_ = secureResponse.Body.Close()
	if string(secureBody) != "secure" {
		t.Fatalf("HTTPS body after plain redirect = %q", secureBody)
	}
}

func TestHTTPSPortRejectsPlainHTTPWhenRedirectIsDisabled(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestPair(t, dir, "localhost")
	manager := NewManager()
	settings := Settings{
		Enabled:         true,
		Listen:          "127.0.0.1:0",
		RedirectHTTP:    false,
		CertificatePath: certPath,
		PrivateKeyPath:  keyPath,
	}
	if err := manager.Start(http.NotFoundHandler(), settings); err != nil {
		t.Fatalf("start HTTPS manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.mu.RLock()
	address := manager.listener.Addr().String()
	manager.mu.RUnlock()
	response, err := http.Get("http://" + address + "/")
	if err != nil {
		t.Fatalf("plain HTTP request to HTTPS port: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain HTTP status = %d", response.StatusCode)
	}
}

func TestDisabledManagerShowsValidatedCertificateMetadata(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestPair(t, dir, "preview.example.com")
	manager := NewManager()
	settings := Settings{
		Enabled:         false,
		Listen:          ":35938",
		CertificatePath: certPath,
		PrivateKeyPath:  keyPath,
	}
	if err := manager.Start(http.NotFoundHandler(), settings); err != nil {
		t.Fatalf("preview disabled HTTPS certificate: %v", err)
	}

	status := manager.Status()
	if status.Running || !status.Ready {
		t.Fatalf("unexpected disabled certificate status: %+v", status)
	}
	if len(status.Domains) != 1 || status.Domains[0] != "preview.example.com" {
		t.Fatalf("preview domains = %v", status.Domains)
	}
	if status.Issuer == "" || status.ExpiresAt.IsZero() || status.Fingerprint == "" {
		t.Fatalf("certificate metadata is incomplete: %+v", status)
	}
}

func TestDisabledManagerAllowsMissingCertificate(t *testing.T) {
	manager := NewManager()
	settings := Settings{
		Enabled:         false,
		Listen:          ":35938",
		CertificatePath: filepath.Join(t.TempDir(), "missing.crt"),
		PrivateKeyPath:  filepath.Join(t.TempDir(), "missing.key"),
	}
	if err := manager.Start(http.NotFoundHandler(), settings); err != nil {
		t.Fatalf("start disabled HTTPS without a certificate: %v", err)
	}
	status := manager.Status()
	if status.Running || status.Ready {
		t.Fatalf("missing certificate unexpectedly became ready: %+v", status)
	}
}

func TestNormalizeUsesDefaultHTTPSPortAndRequiresCertificatePaths(t *testing.T) {
	settings, err := Normalize(Settings{})
	if err != nil {
		t.Fatalf("normalize default HTTPS settings: %v", err)
	}
	if settings.Listen != ":35938" {
		t.Fatalf("default listen = %q", settings.Listen)
	}
	settings.Enabled = true
	if _, err := Normalize(settings); err == nil {
		t.Fatal("expected enabled HTTPS without certificate paths to be rejected")
	}
}

func TestHTTPRedirectUsesPublic443AndHonorsForwardedHTTPS(t *testing.T) {
	manager := NewManager()
	manager.settings = Settings{
		Enabled:      true,
		Listen:       ":35938",
		RedirectHTTP: true,
	}
	manager.status = Status{Running: true, Ready: true}
	handler := manager.HTTPRedirectHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodPost, "http://example.com/api/report?q=1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d", response.Code)
	}
	if cacheControl := response.Header().Get("Cache-Control"); !strings.Contains(cacheControl, "no-store") {
		t.Fatalf("cache control = %q", cacheControl)
	}
	if location := response.Header().Get("Location"); location != "https://example.com/api/report?q=1" {
		t.Fatalf("location = %q", location)
	}

	request = httptest.NewRequest(http.MethodGet, "http://example.com:25774/admin", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if location := response.Header().Get("Location"); location != "https://example.com:35938/admin" {
		t.Fatalf("explicit-port location = %q", location)
	}

	request = httptest.NewRequest(http.MethodGet, "http://internal/admin", nil)
	request.RemoteAddr = "127.0.0.1:43000"
	request.Header.Set("X-Forwarded-Proto", "https")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("forwarded HTTPS status = %d", response.Code)
	}
	if hsts := response.Header().Get("Strict-Transport-Security"); hsts != "max-age=31536000" {
		t.Fatalf("forwarded HTTPS HSTS = %q", hsts)
	}

	request = httptest.NewRequest(http.MethodGet, "http://example.com/admin", nil)
	request.RemoteAddr = "203.0.113.10:43000"
	request.Header.Set("X-Forwarded-Proto", "https")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect {
		t.Fatalf("spoofed forwarded HTTPS status = %d", response.Code)
	}
}

func TestHTTPSSecurityHeadersFollowStrictMode(t *testing.T) {
	manager := NewManager()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := manager.httpsSecurityHandler(next)

	manager.settings.Enabled = true
	manager.settings.RedirectHTTP = true
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.test/admin", nil))
	if hsts := response.Header().Get("Strict-Transport-Security"); hsts != "max-age=31536000" {
		t.Fatalf("strict HSTS = %q", hsts)
	}
	if value := response.Header().Get("X-Content-Type-Options"); value != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", value)
	}
	if value := response.Header().Get("Referrer-Policy"); value != "strict-origin-when-cross-origin" {
		t.Fatalf("Referrer-Policy = %q", value)
	}

	manager.settings.RedirectHTTP = false
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.test/admin", nil))
	if hsts := response.Header().Get("Strict-Transport-Security"); hsts != "max-age=0" {
		t.Fatalf("disabled HSTS = %q", hsts)
	}
}

func TestDisableAfterResponseKeepsTLSAliveUntilResponseCompletes(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestPair(t, dir, "localhost")
	manager := NewManager()
	enabled := Settings{
		Enabled:         true,
		Listen:          "127.0.0.1:0",
		RedirectHTTP:    true,
		CertificatePath: certPath,
		PrivateKeyPath:  keyPath,
	}
	disabled := enabled
	disabled.Enabled = false

	if err := manager.Start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/disable" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := manager.DisableAfterResponse(disabled, 150*time.Millisecond); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Strict-Transport-Security", "max-age=0")
		_, _ = io.WriteString(w, "disabled")
	}), enabled, "0.0.0.0:25774"); err != nil {
		t.Fatalf("start HTTPS manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })

	manager.mu.RLock()
	address := manager.listener.Addr().String()
	manager.mu.RUnlock()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test-only self-signed certificate
	}}
	response, err := client.Get("https://" + address + "/disable")
	if err != nil {
		t.Fatalf("disable response was interrupted: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "disabled" {
		t.Fatalf("disable response body = %q", body)
	}
	if hsts := response.Header.Get("Strict-Transport-Security"); hsts != "max-age=0" {
		t.Fatalf("disable response HSTS = %q", hsts)
	}
	if status := manager.Status(); status.Enabled || !status.Running {
		t.Fatalf("unexpected shutdown grace status: %+v", status)
	}

	redirectHandler := manager.HTTPRedirectHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	redirectHandler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("HTTP still redirected during HTTPS shutdown grace: %d", recorder.Code)
	}

	deadline := time.Now().Add(2 * time.Second)
	for manager.Status().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if status := manager.Status(); status.Running {
		t.Fatalf("HTTPS listener still running after grace period: %+v", status)
	}
	if _, err := client.Get("https://" + address + "/status"); err == nil {
		t.Fatal("HTTPS listener still accepted connections after shutdown")
	}
}

func TestDeferredDisableDoesNotStopReenabledHTTPS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestPair(t, dir, "localhost")
	manager := NewManager()
	enabled := Settings{
		Enabled:         true,
		Listen:          "127.0.0.1:0",
		CertificatePath: certPath,
		PrivateKeyPath:  keyPath,
	}
	if err := manager.Start(http.NotFoundHandler(), enabled); err != nil {
		t.Fatalf("start HTTPS manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	disabled := enabled
	disabled.Enabled = false
	if err := manager.DisableAfterResponse(disabled, 50*time.Millisecond); err != nil {
		t.Fatalf("schedule HTTPS disable: %v", err)
	}
	if err := manager.Apply(enabled); err != nil {
		t.Fatalf("re-enable HTTPS: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if status := manager.Status(); !status.Enabled || !status.Running {
		t.Fatalf("stale deferred disable stopped re-enabled HTTPS: %+v", status)
	}
}

func TestHTTPOriginOnlyForDirectTLS(t *testing.T) {
	manager := NewManager()
	manager.httpListen = "0.0.0.0:25774"

	direct := httptest.NewRequest(http.MethodPost, "https://example.test:35938/api/admin/settings/https", nil)
	if origin := manager.HTTPOrigin(direct); origin != "http://example.test:25774" {
		t.Fatalf("direct TLS HTTP origin = %q", origin)
	}
	directIPv6 := httptest.NewRequest(http.MethodPost, "https://[2001:db8::10]:35938/api/admin/settings/https", nil)
	if origin := manager.HTTPOrigin(directIPv6); origin != "http://[2001:db8::10]:25774" {
		t.Fatalf("direct IPv6 TLS HTTP origin = %q", origin)
	}
	forwarded := httptest.NewRequest(http.MethodPost, "http://internal/api/admin/settings/https", nil)
	forwarded.RemoteAddr = "127.0.0.1:43000"
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	if origin := manager.HTTPOrigin(forwarded); origin != "" {
		t.Fatalf("reverse-proxied HTTPS received unsafe HTTP origin %q", origin)
	}
	forwardedOverTLS := httptest.NewRequest(http.MethodPost, "https://example.test/api/admin/settings/https", nil)
	forwardedOverTLS.RemoteAddr = "172.18.0.3:43000"
	forwardedOverTLS.Header.Set("X-Forwarded-Proto", "https")
	if origin := manager.HTTPOrigin(forwardedOverTLS); origin != "" {
		t.Fatalf("TLS reverse proxy received unsafe HTTP origin %q", origin)
	}
}

func TestHTTPSOriginOnlyForDirectHTTP(t *testing.T) {
	manager := NewManager()
	manager.settings = Settings{Listen: ":35938"}

	direct := httptest.NewRequest(http.MethodPost, "http://example.test:25881/api/admin/settings/https", nil)
	if origin := manager.HTTPSOrigin(direct); origin != "https://example.test:35938" {
		t.Fatalf("direct HTTP HTTPS origin = %q", origin)
	}
	directIPv6 := httptest.NewRequest(http.MethodPost, "http://[2001:db8::10]:25881/api/admin/settings/https", nil)
	if origin := manager.HTTPSOrigin(directIPv6); origin != "https://[2001:db8::10]:35938" {
		t.Fatalf("direct IPv6 HTTP HTTPS origin = %q", origin)
	}
	secure := httptest.NewRequest(http.MethodPost, "https://example.test:35938/api/admin/settings/https", nil)
	if origin := manager.HTTPSOrigin(secure); origin != "" {
		t.Fatalf("direct TLS received redundant HTTPS origin %q", origin)
	}
	forwarded := httptest.NewRequest(http.MethodPost, "http://internal/api/admin/settings/https", nil)
	forwarded.RemoteAddr = "127.0.0.1:43000"
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	if origin := manager.HTTPSOrigin(forwarded); origin != "" {
		t.Fatalf("reverse-proxied HTTPS received built-in HTTPS origin %q", origin)
	}
}

func TestListenerFamilyAvailabilityIgnoresMissingAndLoopbackFamilies(t *testing.T) {
	tests := []struct {
		name     string
		ips      []net.IP
		wantIPv4 bool
		wantIPv6 bool
	}{
		{name: "loopback only", ips: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}},
		{name: "IPv4 only", ips: []net.IP{net.ParseIP("192.0.2.10")}, wantIPv4: true},
		{name: "IPv6 only", ips: []net.IP{net.ParseIP("2001:db8::10")}, wantIPv6: true},
		{name: "dual stack", ips: []net.IP{net.ParseIP("10.0.0.10"), net.ParseIP("fd00::10")}, wantIPv4: true, wantIPv6: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ipv4, ipv6 := listenerFamilyAvailabilityFromIPs(test.ips)
			if ipv4 != test.wantIPv4 || ipv6 != test.wantIPv6 {
				t.Fatalf("availability = IPv4:%t IPv6:%t", ipv4, ipv6)
			}
		})
	}
}

func TestRedirectWaitsForReadyHTTPS(t *testing.T) {
	manager := NewManager()
	manager.settings = Settings{Enabled: true, Listen: ":35938", RedirectHTTP: true}
	manager.status = Status{Running: true, Ready: false}
	handler := manager.HTTPRedirectHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://example.test/admin", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("unready HTTPS status = %d", response.Code)
	}
}

func TestFailedHotReloadKeepsPreviousCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := writeTestPair(t, dir, "first.example.com")
	provider, err := newManualProvider(certPath, keyPath)
	if err != nil {
		t.Fatalf("load first pair: %v", err)
	}
	manager := NewManager()
	manager.provider = provider
	manager.status = statusFrom(Settings{Enabled: true}, provider, true, "")
	before := manager.Status().Fingerprint

	secondCert, secondKey := makeTestPair(t, "second.example.com")
	if err := os.WriteFile(certPath, secondCert, 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReloadCertificate(); err == nil {
		t.Fatal("expected mismatched certificate reload to fail")
	}
	if afterFailure := manager.Status().Fingerprint; afterFailure != before {
		t.Fatalf("failed reload replaced the certificate: %q -> %q", before, afterFailure)
	}

	if err := os.WriteFile(keyPath, secondKey, 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReloadCertificate(); err != nil {
		t.Fatalf("reload matching pair: %v", err)
	}
	if afterSuccess := manager.Status().Fingerprint; afterSuccess == before {
		t.Fatal("successful reload did not replace the certificate")
	}
}

func writeTestPair(t *testing.T, dir, domain string) (string, string) {
	t.Helper()
	certPEM, keyPEM := makeTestPair(t, domain)
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func makeTestPair(t *testing.T, domain string) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}
