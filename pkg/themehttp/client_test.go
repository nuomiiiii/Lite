package themehttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testPublicIP = "8.8.8.8"

type harness struct {
	client    *Client
	hostURL   string
	dialed    []string
	lookups   []string
	lookupN   atomic.Int32
	lookupMap map[string][]netip.Addr
}

func newHarness(t *testing.T, handler http.Handler, useTLS bool) *harness {
	t.Helper()
	var srv *httptest.Server
	if useTLS {
		srv = httptest.NewTLSServer(handler)
	} else {
		srv = httptest.NewServer(handler)
	}
	t.Cleanup(srv.Close)

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		lookupMap: map[string][]netip.Addr{
			"theme.test": {netip.MustParseAddr(testPublicIP)},
		},
	}
	var mu sync.Mutex
	scheme := "http"
	tlsCfg := (*tls.Config)(nil)
	if useTLS {
		scheme = "https"
		tlsCfg = &tls.Config{InsecureSkipVerify: true}
	}
	h.hostURL = fmt.Sprintf("%s://theme.test:%s", scheme, port)
	h.client = NewClient(Client{
		Timeout:         5 * time.Second,
		TLSClientConfig: tlsCfg,
		LookupNetIP: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			h.lookupN.Add(1)
			mu.Lock()
			h.lookups = append(h.lookups, host)
			mu.Unlock()
			if addrs, ok := h.lookupMap[host]; ok {
				return append([]netip.Addr(nil), addrs...), nil
			}
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			h.dialed = append(h.dialed, address)
			mu.Unlock()
			host, dialPort, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if host != testPublicIP {
				return nil, fmt.Errorf("dialed unexpected host %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", dialPort))
		},
	})
	return h
}

func (h *harness) get(t *testing.T, path string, maxBytes int64) ([]byte, error) {
	t.Helper()
	return h.client.DownloadBytes(context.Background(), h.hostURL+path, maxBytes)
}

func TestDownloadHTTPAndHTTPSPublicHost(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "" && !strings.HasPrefix(r.Host, "theme.test") {
			t.Errorf("Host = %q, want theme.test", r.Host)
		}
		_, _ = w.Write([]byte("theme-ok"))
	})
	for _, tlsMode := range []bool{false, true} {
		h := newHarness(t, handler, tlsMode)
		data, err := h.get(t, "/", 64)
		if err != nil {
			t.Fatalf("tls=%v: %v", tlsMode, err)
		}
		if string(data) != "theme-ok" {
			t.Fatalf("tls=%v body = %q", tlsMode, data)
		}
		if len(h.dialed) == 0 || !strings.HasPrefix(h.dialed[0], testPublicIP+":") {
			t.Fatalf("tls=%v dialed %v, want pinned public IP", tlsMode, h.dialed)
		}
	}
}

func TestRejectsSchemeAndUserinfo(t *testing.T) {
	client := New()
	cases := []struct {
		raw  string
		want error
	}{
		{"ftp://example.com/a.zip", ErrUnsupportedScheme},
		{"file:///tmp/a.zip", ErrUnsupportedScheme},
		{"http://user:secret@example.com/a.zip", ErrInvalidURL},
		{"https://token@example.com/a.zip", ErrInvalidURL},
		{"not a url", ErrInvalidURL},
		{"http://", ErrInvalidURL},
	}
	for _, tc := range cases {
		_, err := client.DownloadBytes(context.Background(), tc.raw, 64)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.raw, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: error leaked credential: %v", tc.raw, err)
		}
	}
}

func TestRejectsLiteralPrivateIPs(t *testing.T) {
	client := New()
	urls := []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://192.168.0.1/",
		"http://169.254.169.254/",
		"http://100.64.0.1/",
		"http://192.0.2.1/",
		"http://198.51.100.1/",
		"http://203.0.113.1/",
		"http://[::1]/",
		"http://[2001:db8::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[::ffff:192.168.1.1]/",
	}
	for _, raw := range urls {
		_, err := client.DownloadBytes(context.Background(), raw, 64)
		if !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("%s: err = %v, want ErrPrivateAddress", raw, err)
		}
	}
}

func TestRejectsMixedPublicAndPrivateDNS(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should-not-reach"))
	}), false)
	h.lookupMap["theme.test"] = []netip.Addr{
		netip.MustParseAddr(testPublicIP),
		netip.MustParseAddr("10.0.0.1"),
	}
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("err = %v, want ErrPrivateAddress", err)
	}
	if len(h.dialed) != 0 {
		t.Fatalf("dialed %v, want no dial", h.dialed)
	}
}

func TestPinsDialToResolvedIPs(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), false)
	if _, err := h.get(t, "/", 64); err != nil {
		t.Fatal(err)
	}
	if h.lookupN.Load() != 1 {
		t.Fatalf("lookups = %d, want 1", h.lookupN.Load())
	}
	if len(h.dialed) != 1 || !strings.HasPrefix(h.dialed[0], testPublicIP+":") {
		t.Fatalf("dialed %v", h.dialed)
	}
}

func TestDNSRebindingDoesNotDialLaterPrivateIP(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), false)
	var n int
	h.client = NewClient(Client{
		Timeout: 5 * time.Second,
		LookupNetIP: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			n++
			if n == 1 {
				return []netip.Addr{netip.MustParseAddr(testPublicIP)}, nil
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		DialContext: h.client.DialContext,
	})
	if _, err := h.get(t, "/", 64); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("lookups = %d, want 1 (second private answer must not be used)", n)
	}
}

func TestRedirectToPrivateIsRejected(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/secret", http.StatusFound)
	}), false)
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("err = %v, want ErrPrivateAddress", err)
	}
}

func TestRedirectToNonHTTPIsRejected(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "ftp://example.com/a.zip")
		w.WriteHeader(http.StatusFound)
	}), false)
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("err = %v, want ErrUnsupportedScheme", err)
	}
}

func TestTooManyRedirects(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}), false)
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
}

func TestContentLengthOverLimit(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tiny"))
	}), false)
	_, err := h.get(t, "/", 8)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestChunkedOverflow(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Transfer-Encoding", "chunked")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 20; i++ {
			_, _ = w.Write([]byte("x"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}), false)
	_, err := h.get(t, "/", 8)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestExactLimitIsAllowed(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 8)
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}), false)
	data, err := h.get(t, "/", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, body) {
		t.Fatalf("got %q", data)
	}
}

func TestEmptyResponseRejected(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), false)
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
}

func TestNon2xxRejected(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}), false)
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrHTTPStatus) {
		t.Fatalf("err = %v, want ErrHTTPStatus", err)
	}
	if strings.Contains(err.Error(), "nope") {
		t.Fatalf("error leaked body: %v", err)
	}
}

func TestJSONAndPreviewCaps(t *testing.T) {
	if MaxGitHubJSON != MaxCatalog {
		t.Fatal("GitHub JSON and catalog caps should both be 2 MiB")
	}
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", MaxPreview+1))
		w.WriteHeader(http.StatusOK)
	}), false)
	_, err := h.client.DownloadBytes(context.Background(), h.hostURL+"/", MaxPreview)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("preview cap: %v", err)
	}
	_, err = h.client.DownloadBytes(context.Background(), h.hostURL+"/", MaxCatalog)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("catalog cap: %v", err)
	}
	_, err = h.client.DownloadBytes(context.Background(), h.hostURL+"/", MaxGitHubJSON)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("github json cap: %v", err)
	}
}

func TestDownloadFileHashesAndCleansUp(t *testing.T) {
	body := []byte("theme-zip-bytes")
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write(body)
		case "/empty":
			w.WriteHeader(http.StatusOK)
		default:
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("x"))
		}
	}), false)
	prefix := "lite-theme-" + t.Name()
	path, sum, err := h.client.DownloadFile(context.Background(), h.hostURL+"/ok", 64, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	want := sha256.Sum256(body)
	if !bytes.Equal(sum, want[:]) {
		t.Fatalf("sha256 mismatch")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("file content mismatch")
	}

	_, _, err = h.client.DownloadFile(context.Background(), h.hostURL+"/empty", 64, prefix+"-empty")
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}
	emptyMatches, _ := filepath.Glob(filepath.Join(os.TempDir(), prefix+"-empty-*.part"))
	if len(emptyMatches) != 0 {
		t.Fatalf("leftover empty temp files: %v", emptyMatches)
	}
	_, _, err = h.client.DownloadFile(context.Background(), h.hostURL+"/big", 8, prefix+"-fail")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
	failMatches, _ := filepath.Glob(filepath.Join(os.TempDir(), prefix+"-fail-*.part"))
	if len(failMatches) != 0 {
		t.Fatalf("leftover temp files: %v", failMatches)
	}
}

func TestNoProxyBypass(t *testing.T) {
	proxyHits := atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		_, _ = w.Write([]byte("from-proxy"))
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("from-target"))
	}), false)
	data, err := h.get(t, "/", 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "from-target" {
		t.Fatalf("body = %q, proxy may have been used", data)
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("proxy was used %d times", proxyHits.Load())
	}
}

func TestDNSFailure(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), false)
	delete(h.lookupMap, "theme.test")
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrDNS) {
		t.Fatalf("err = %v, want ErrDNS", err)
	}
}

func TestTimeout(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}), false)
	h.client = NewClient(Client{
		Timeout:         50 * time.Millisecond,
		LookupNetIP:     h.client.LookupNetIP,
		DialContext:     h.client.DialContext,
		TLSClientConfig: h.client.TLSClientConfig,
	})
	_, err := h.get(t, "/", 64)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestTransportDisablesEnvProxy(t *testing.T) {
	client := New()
	tr, ok := client.httpClient.Transport.(*pinningTransport)
	if !ok {
		t.Fatal("expected pinningTransport")
	}
	proxyURL, err := tr.inner.Proxy(&http.Request{URL: &url.URL{Scheme: "http", Host: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL != nil {
		t.Fatalf("Proxy = %v, want nil", proxyURL)
	}
}

func TestDownloadFileUsesArchiveCapConstant(t *testing.T) {
	body := []byte("zip")
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}), false)
	path, _, err := h.client.DownloadFile(context.Background(), h.hostURL+"/", MaxArchive, "lite-theme-cap")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	if MaxArchive != 128<<20 {
		t.Fatalf("theme ZIP download cap must stay 128 MiB")
	}
}
