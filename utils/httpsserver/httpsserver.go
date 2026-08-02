package httpsserver

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/pkg/config"
	logger "github.com/komari-monitor/komari/utils/log"
	"github.com/komari-monitor/komari/utils/requestscheme"
)

type Settings struct {
	Enabled         bool   `json:"https_enabled" default:"false"`
	Listen          string `json:"https_listen" default:":35938"`
	RedirectHTTP    bool   `json:"https_redirect_http" default:"false"`
	CertificatePath string `json:"https_certificate_path" default:"./data/tls/server.crt"`
	PrivateKeyPath  string `json:"https_private_key_path" default:"./data/tls/server.key"`
}

type Status struct {
	Enabled               bool      `json:"enabled"`
	Running               bool      `json:"running"`
	Ready                 bool      `json:"ready"`
	ListenerIPv4          bool      `json:"listener_ipv4"`
	ListenerIPv6          bool      `json:"listener_ipv6"`
	ListenerIPv4Available bool      `json:"listener_ipv4_available"`
	ListenerIPv6Available bool      `json:"listener_ipv6_available"`
	ListenerProbeDone     bool      `json:"listener_probe_done"`
	Listen                string    `json:"listen"`
	Domains               []string  `json:"domains"`
	Issuer                string    `json:"issuer,omitempty"`
	ExpiresAt             time.Time `json:"expires_at,omitempty"`
	LastCheckedAt         time.Time `json:"last_checked_at,omitempty"`
	LastLoadedAt          time.Time `json:"last_loaded_at,omitempty"`
	Fingerprint           string    `json:"fingerprint,omitempty"`
	Error                 string    `json:"error,omitempty"`
}

type certificateProvider interface {
	GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
	Metadata() certificateMetadata
}

type certificateMetadata struct {
	Domains     []string
	Issuer      string
	ExpiresAt   time.Time
	Fingerprint string
	LoadedAt    time.Time
	Ready       bool
}

type manualProvider struct {
	mu       sync.RWMutex
	certPath string
	keyPath  string
	cert     *tls.Certificate
	meta     certificateMetadata
	hash     string
}

func newManualProvider(certPath, keyPath string) (*manualProvider, error) {
	p := &manualProvider{certPath: certPath, keyPath: keyPath}
	if _, err := p.reload(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *manualProvider) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cert == nil {
		return nil, errors.New("TLS certificate is not loaded")
	}
	return p.cert, nil
}

func (p *manualProvider) Metadata() certificateMetadata {
	p.mu.RLock()
	defer p.mu.RUnlock()
	meta := p.meta
	meta.Domains = append([]string(nil), meta.Domains...)
	return meta
}

func (p *manualProvider) reload() (bool, error) {
	certPEM, err := os.ReadFile(p.certPath)
	if err != nil {
		return false, fmt.Errorf("read certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(p.keyPath)
	if err != nil {
		return false, fmt.Errorf("read private key: %w", err)
	}
	sum := sha256.Sum256(append(append([]byte(nil), certPEM...), keyPEM...))
	hash := hex.EncodeToString(sum[:])

	p.mu.RLock()
	unchanged := p.hash == hash && p.cert != nil
	p.mu.RUnlock()
	if unchanged {
		return false, nil
	}

	cert, meta, err := parseCertificatePair(certPEM, keyPEM)
	if err != nil {
		return false, err
	}
	meta.LoadedAt = time.Now().UTC()
	meta.Ready = true
	p.mu.Lock()
	p.cert = cert
	p.meta = meta
	p.hash = hash
	p.mu.Unlock()
	return true, nil
}

type Manager struct {
	applyMu sync.Mutex
	mu      sync.RWMutex

	handler        http.Handler
	httpListen     string
	settings       Settings
	provider       certificateProvider
	server         *http.Server
	redirectServer *http.Server
	listener       net.Listener
	watchStop      context.CancelFunc
	status         Status
}

type protocolMux struct {
	listener  net.Listener
	tlsConns  chan net.Conn
	httpConns chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

type protocolListener struct {
	addr  net.Addr
	conns <-chan net.Conn
	done  <-chan struct{}
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func newProtocolMux(listener net.Listener) *protocolMux {
	mux := &protocolMux{
		listener:  listener,
		tlsConns:  make(chan net.Conn, 64),
		httpConns: make(chan net.Conn, 64),
		done:      make(chan struct{}),
	}
	go mux.accept()
	return mux
}

func (m *protocolMux) accept() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			m.closeOnce.Do(func() { close(m.done) })
			return
		}
		go m.classify(conn)
	}
}

func (m *protocolMux) classify(conn net.Conn) {
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	first, err := reader.Peek(1)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}
	wrapped := &bufferedConn{Conn: conn, reader: reader}
	target := m.httpConns
	if first[0] == 0x16 {
		target = m.tlsConns
	}
	select {
	case target <- wrapped:
	case <-m.done:
		_ = conn.Close()
	}
}

func (m *protocolMux) tlsListener() net.Listener {
	return &protocolListener{addr: m.listener.Addr(), conns: m.tlsConns, done: m.done}
}

func (m *protocolMux) httpListener() net.Listener {
	return &protocolListener{addr: m.listener.Addr(), conns: m.httpConns, done: m.done}
}

func (l *protocolListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case conn := <-l.conns:
		return conn, nil
	}
}

func (l *protocolListener) Close() error   { return nil }
func (l *protocolListener) Addr() net.Addr { return l.addr }

var Default = NewManager()

func NewManager() *Manager {
	return &Manager{}
}

func LoadSettings() (Settings, error) {
	settings, err := config.GetManyAs[Settings]()
	if err != nil {
		return Settings{}, err
	}
	return Normalize(*settings)
}

func Normalize(settings Settings) (Settings, error) {
	settings.Listen = strings.TrimSpace(settings.Listen)
	if settings.Listen == "" {
		settings.Listen = ":35938"
	}
	if !strings.Contains(settings.Listen, ":") {
		settings.Listen = ":" + settings.Listen
	}
	host, port, err := net.SplitHostPort(settings.Listen)
	if err != nil {
		return Settings{}, fmt.Errorf("invalid HTTPS listen address: %w", err)
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return Settings{}, fmt.Errorf("invalid HTTPS listen port: %w", err)
	}
	settings.Listen = net.JoinHostPort(host, port)

	settings.CertificatePath = strings.TrimSpace(settings.CertificatePath)
	settings.PrivateKeyPath = strings.TrimSpace(settings.PrivateKeyPath)
	if settings.Enabled && (settings.CertificatePath == "" || settings.PrivateKeyPath == "") {
		return Settings{}, errors.New("certificate and private key paths are required")
	}
	return settings, nil
}

func (settings Settings) ConfigMap() map[string]any {
	return map[string]any{
		config.HTTPSEnabledKey:         settings.Enabled,
		config.HTTPSListenKey:          settings.Listen,
		config.HTTPSRedirectHTTPKey:    settings.RedirectHTTP,
		config.HTTPSCertificatePathKey: settings.CertificatePath,
		config.HTTPSPrivateKeyPathKey:  settings.PrivateKeyPath,
	}
}

func (m *Manager) Start(handler http.Handler, settings Settings, httpListen ...string) error {
	m.mu.Lock()
	m.handler = handler
	if len(httpListen) > 0 {
		m.httpListen = strings.TrimSpace(httpListen[0])
	}
	m.mu.Unlock()
	return m.Apply(settings)
}

// DisableAfterResponse makes redirect and HSTS policy safe immediately, then
// leaves the TLS listener alive briefly so the settings response can arrive.
func (m *Manager) DisableAfterResponse(settings Settings, delay time.Duration) error {
	normalized, err := Normalize(settings)
	if err != nil {
		return err
	}
	if normalized.Enabled {
		return errors.New("deferred HTTPS shutdown requires HTTPS to be disabled")
	}
	provider, providerErr := buildProvider(normalized)
	errText := ""
	if providerErr != nil {
		errText = providerErr.Error()
		provider = nil
	}

	m.applyMu.Lock()
	m.mu.Lock()
	if m.handler == nil {
		m.mu.Unlock()
		m.applyMu.Unlock()
		return errors.New("HTTPS server is not initialized")
	}
	m.stopWatcherLocked()
	server := m.server
	redirectServer := m.redirectServer
	listener := m.listener
	previousStatus := m.status
	m.settings = normalized
	m.provider = provider
	m.status = statusFrom(normalized, provider, server != nil, errText)
	if server != nil {
		m.status.ListenerIPv4 = previousStatus.ListenerIPv4
		m.status.ListenerIPv6 = previousStatus.ListenerIPv6
		m.status.ListenerProbeDone = previousStatus.ListenerProbeDone
	}
	m.mu.Unlock()
	m.applyMu.Unlock()

	if server == nil {
		return nil
	}
	if delay < 0 {
		delay = 0
	}
	time.AfterFunc(delay, func() {
		m.applyMu.Lock()
		defer m.applyMu.Unlock()

		m.mu.Lock()
		if m.server != server || m.settings.Enabled {
			m.mu.Unlock()
			return
		}
		currentSettings := m.settings
		currentProvider := m.provider
		m.server = nil
		m.redirectServer = nil
		m.listener = nil
		m.status = statusFrom(currentSettings, currentProvider, false, errText)
		m.mu.Unlock()

		shutdownServers(server, redirectServer, listener)
		logger.Infof("https", "Built-in HTTPS server was disabled after the settings response completed")
	})
	return nil
}

func (m *Manager) Apply(settings Settings) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()

	normalized, err := Normalize(settings)
	if err != nil {
		m.setError(settings, err)
		return err
	}
	provider, providerErr := buildProvider(normalized)
	if providerErr != nil && normalized.Enabled {
		err = providerErr
		m.setError(normalized, err)
		return err
	}

	m.mu.RLock()
	handler := m.handler
	oldServer := m.server
	oldRedirectServer := m.redirectServer
	oldListener := m.listener
	oldListen := m.settings.Listen
	m.mu.RUnlock()
	if handler == nil {
		return errors.New("HTTPS server is not initialized")
	}

	if !normalized.Enabled {
		errText := ""
		if providerErr != nil {
			errText = providerErr.Error()
			provider = nil
		}
		m.mu.Lock()
		m.stopWatcherLocked()
		m.settings = normalized
		m.provider = provider
		m.server = nil
		m.redirectServer = nil
		m.listener = nil
		m.status = statusFrom(normalized, provider, false, errText)
		m.mu.Unlock()
		shutdownServers(oldServer, oldRedirectServer, oldListener)
		return nil
	}

	if oldServer != nil && oldListen == normalized.Listen {
		m.mu.Lock()
		m.stopWatcherLocked()
		m.settings = normalized
		m.provider = provider
		m.status = statusFrom(normalized, provider, true, "")
		m.startWatcherLocked(provider)
		m.mu.Unlock()
		go m.probeListenerFamilies(oldServer, normalized, oldListener)
		return nil
	}

	listener, err := net.Listen("tcp", normalized.Listen)
	if err != nil {
		err = fmt.Errorf("listen on %s: %w", normalized.Listen, err)
		m.setError(normalized, err)
		return err
	}
	server := &http.Server{
		Addr:              normalized.Listen,
		Handler:           m.httpsSecurityHandler(handler),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	redirectServer := &http.Server{
		Addr:              normalized.Listen,
		Handler:           m.httpsPortRedirectHandler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return m.getCertificate(hello)
		},
	}
	protocols := newProtocolMux(listener)
	tlsListener := tls.NewListener(protocols.tlsListener(), tlsConfig)
	httpListener := protocols.httpListener()

	m.mu.Lock()
	m.stopWatcherLocked()
	m.settings = normalized
	m.provider = provider
	m.server = server
	m.redirectServer = redirectServer
	m.listener = listener
	m.status = statusFrom(normalized, provider, true, "")
	m.startWatcherLocked(provider)
	m.mu.Unlock()

	go m.serve(server, redirectServer, listener, tlsListener, httpListener)
	go m.probeListenerFamilies(server, normalized, listener)
	shutdownServers(oldServer, oldRedirectServer, oldListener)
	logger.Infof("https", "Built-in HTTPS server is listening on %s", normalized.Listen)
	return nil
}

func (m *Manager) httpsSecurityHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.setSecurityHeaders(w, true)
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) httpsPortRedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.setSecurityHeaders(w, false)
		if !m.shouldRedirect(r) {
			http.Error(w, "Client sent an HTTP request to an HTTPS server.", http.StatusBadRequest)
			return
		}
		location, err := m.redirectURL(r)
		if err != nil {
			http.Error(w, "Invalid HTTPS redirect target.", http.StatusBadRequest)
			return
		}
		redirectWithoutCache(w, r, location)
	})
}

func (m *Manager) probeListenerFamilies(server *http.Server, settings Settings, listener net.Listener) {
	host, port, err := net.SplitHostPort(settings.Listen)
	if err != nil {
		return
	}
	if port == "0" {
		if _, activePort, splitErr := net.SplitHostPort(listener.Addr().String()); splitErr == nil {
			port = activePort
		}
	}

	var ipv4, ipv6 bool
	switch ip := net.ParseIP(strings.Trim(host, "[]")); {
	case host == "":
		ipv4 = probeTLSListener("tcp4", net.JoinHostPort("127.0.0.1", port))
		ipv6 = probeTLSListener("tcp6", net.JoinHostPort("::1", port))
	case ip != nil && ip.To4() != nil:
		probeHost := host
		if ip.IsUnspecified() {
			probeHost = "127.0.0.1"
		}
		ipv4 = probeTLSListener("tcp4", net.JoinHostPort(probeHost, port))
	case ip != nil:
		probeHost := strings.Trim(host, "[]")
		if ip.IsUnspecified() {
			probeHost = "::1"
		}
		ipv6 = probeTLSListener("tcp6", net.JoinHostPort(probeHost, port))
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
		cancel()
		if lookupErr == nil {
			for _, address := range addresses {
				if address.IP.To4() != nil && !ipv4 {
					ipv4 = probeTLSListener("tcp4", net.JoinHostPort(address.IP.String(), port))
				} else if address.IP.To4() == nil && !ipv6 {
					ipv6 = probeTLSListener("tcp6", net.JoinHostPort(address.IP.String(), port))
				}
			}
		}
	}

	m.mu.Lock()
	if m.server == server {
		m.status.ListenerIPv4 = ipv4
		m.status.ListenerIPv6 = ipv6
		m.status.ListenerProbeDone = true
	}
	m.mu.Unlock()
}

func listenerFamilyAvailability() (bool, bool) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return protocolStackAvailable("tcp4", "127.0.0.1:0"), protocolStackAvailable("tcp6", "[::1]:0")
	}
	var ips []net.IP
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr != nil {
				ip = net.ParseIP(strings.Split(address.String(), "%")[0])
			}
			if ip == nil || !ip.IsGlobalUnicast() {
				continue
			}
			ips = append(ips, ip)
		}
	}
	return listenerFamilyAvailabilityFromIPs(ips)
}

func listenerFamilyAvailabilityFromIPs(ips []net.IP) (bool, bool) {
	var ipv4, ipv6 bool
	for _, ip := range ips {
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
			continue
		}
		if ip.To4() != nil {
			ipv4 = true
		} else {
			ipv6 = true
		}
	}
	return ipv4, ipv6
}

func protocolStackAvailable(network, address string) bool {
	listener, err := net.Listen(network, address)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func probeTLSListener(network, address string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dialer := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 2 * time.Second},
		Config: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // Runtime reachability probe; certificate validity is checked when loaded.
		},
	}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (m *Manager) setSecurityHeaders(w http.ResponseWriter, secure bool) {
	m.mu.RLock()
	strictHTTPS := m.settings.Enabled && m.settings.RedirectHTTP
	m.mu.RUnlock()
	if secure {
		if strictHTTPS {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		} else {
			w.Header().Set("Strict-Transport-Security", "max-age=0")
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
}

func buildProvider(settings Settings) (certificateProvider, error) {
	if !settings.Enabled {
		available, err := certificatePairAvailable(settings.CertificatePath, settings.PrivateKeyPath)
		if err != nil || !available {
			return nil, err
		}
	}
	return newManualProvider(settings.CertificatePath, settings.PrivateKeyPath)
}

func certificatePairAvailable(certPath, keyPath string) (bool, error) {
	for _, item := range []struct {
		label string
		path  string
	}{
		{label: "certificate", path: strings.TrimSpace(certPath)},
		{label: "private key", path: strings.TrimSpace(keyPath)},
	} {
		if item.path == "" {
			return false, nil
		}
		if _, err := os.Stat(item.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, fmt.Errorf("inspect %s: %w", item.label, err)
		}
	}
	return true, nil
}

func (m *Manager) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	provider := m.provider
	m.mu.RUnlock()
	if provider == nil {
		return nil, errors.New("HTTPS certificate provider is not configured")
	}
	cert, err := provider.GetCertificate(hello)
	if err != nil {
		m.mu.Lock()
		m.status.Error = err.Error()
		m.status.LastCheckedAt = time.Now().UTC()
		m.mu.Unlock()
		return nil, err
	}
	m.refreshStatus(provider, "")
	return cert, nil
}

func (m *Manager) serve(server, redirectServer *http.Server, listener, tlsListener, httpListener net.Listener) {
	type serveResult struct {
		name string
		err  error
	}
	results := make(chan serveResult, 2)
	go func() { results <- serveResult{name: "HTTPS", err: server.Serve(tlsListener)} }()
	go func() { results <- serveResult{name: "HTTP redirect", err: redirectServer.Serve(httpListener)} }()

	first := <-results
	if first.err == nil || errors.Is(first.err, http.ErrServerClosed) || errors.Is(first.err, net.ErrClosed) {
		return
	}
	_ = listener.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = shutdownHTTPServers(shutdownCtx, server, redirectServer)
	cancel()
	<-results
	m.mu.Lock()
	if m.server == server {
		m.status.Running = false
		m.status.Ready = false
		m.status.ListenerIPv4 = false
		m.status.ListenerIPv6 = false
		m.status.ListenerProbeDone = true
		m.status.Error = first.err.Error()
		m.server = nil
		m.redirectServer = nil
		m.listener = nil
	}
	m.mu.Unlock()
	logger.Errorf("https", "Built-in %s server stopped: %v", first.name, first.err)
}

func (m *Manager) startWatcherLocked(provider certificateProvider) {
	manual, ok := provider.(*manualProvider)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.watchStop = cancel
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.reloadManual(manual)
			}
		}
	}()
}

func (m *Manager) stopWatcherLocked() {
	if m.watchStop != nil {
		m.watchStop()
		m.watchStop = nil
	}
}

func (m *Manager) reloadManual(provider *manualProvider) {
	var changed bool
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		changed, err = provider.reload()
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		m.refreshStatus(provider, err.Error())
		logger.Errorf("https", "TLS certificate reload failed; continuing with the previous certificate: %v", err)
		return
	}
	m.refreshStatus(provider, "")
	if changed {
		logger.Infof("https", "TLS certificate was reloaded without restarting Komari")
	}
}

func (m *Manager) ReloadCertificate() error {
	m.mu.RLock()
	provider := m.provider
	m.mu.RUnlock()
	manual, ok := provider.(*manualProvider)
	if !ok {
		return errors.New("certificate paths are not configured")
	}
	m.reloadManual(manual)
	m.mu.RLock()
	errText := m.status.Error
	m.mu.RUnlock()
	if errText != "" {
		return errors.New(errText)
	}
	return nil
}

func (m *Manager) refreshStatus(provider certificateProvider, errText string) {
	meta := provider.Metadata()
	m.mu.Lock()
	m.status.Domains = meta.Domains
	m.status.Issuer = meta.Issuer
	m.status.ExpiresAt = meta.ExpiresAt
	m.status.Fingerprint = meta.Fingerprint
	m.status.LastLoadedAt = meta.LoadedAt
	m.status.LastCheckedAt = time.Now().UTC()
	m.status.Ready = meta.Ready
	m.status.Error = errText
	m.mu.Unlock()
}

func (m *Manager) setError(settings Settings, err error) {
	m.mu.Lock()
	m.settings = settings
	m.status = statusFrom(settings, m.provider, m.server != nil, err.Error())
	m.mu.Unlock()
}

func statusFrom(settings Settings, provider certificateProvider, running bool, errText string) Status {
	ipv4Available, ipv6Available := listenerFamilyAvailability()
	status := Status{
		Enabled:               settings.Enabled,
		Running:               running,
		Listen:                settings.Listen,
		LastCheckedAt:         time.Now().UTC(),
		ListenerIPv4Available: ipv4Available,
		ListenerIPv6Available: ipv6Available,
		ListenerProbeDone:     !running,
		Error:                 errText,
	}
	if provider != nil {
		meta := provider.Metadata()
		status.Domains = meta.Domains
		status.Issuer = meta.Issuer
		status.ExpiresAt = meta.ExpiresAt
		status.LastLoadedAt = meta.LoadedAt
		status.Fingerprint = meta.Fingerprint
		status.Ready = meta.Ready
	}
	return status
}

// HTTPOrigin returns the direct HTTP origin for a request received by the
// built-in TLS listener. Forwarded HTTPS stays on the reverse proxy origin.
func (m *Manager) HTTPOrigin(r *http.Request) string {
	if r == nil || r.TLS == nil || requestscheme.IsForwardedHTTPS(r) {
		return ""
	}
	m.mu.RLock()
	httpListen := m.httpListen
	m.mu.RUnlock()
	listenHost, port, err := net.SplitHostPort(httpListen)
	if err != nil || port == "" || port == "0" {
		return ""
	}

	host := r.URL.Hostname()
	if host == "" {
		host = r.Host
		if parsedHost, _, splitErr := net.SplitHostPort(r.Host); splitErr == nil {
			host = parsedHost
		} else {
			host = strings.Trim(host, "[]")
		}
	}
	if host == "" {
		host = strings.Trim(listenHost, "[]")
	}
	if host == "" || net.ParseIP(host) != nil && net.ParseIP(host).IsUnspecified() {
		return ""
	}
	if port != "80" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	return (&url.URL{Scheme: "http", Host: host}).String()
}

// HTTPSOrigin returns the built-in HTTPS origin for a direct HTTP request.
// Requests already secured by TLS or a reverse proxy stay on their origin.
func (m *Manager) HTTPSOrigin(r *http.Request) string {
	if r == nil || requestscheme.IsHTTPS(r) {
		return ""
	}
	target, err := m.redirectURL(r)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: parsed.Host}).String()
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := m.status
	status.Domains = append([]string(nil), status.Domains...)
	return status
}

func (m *Manager) HTTPRedirectHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure := requestscheme.IsHTTPS(r)
		m.setSecurityHeaders(w, secure)
		if !m.shouldRedirect(r) {
			next.ServeHTTP(w, r)
			return
		}
		location, err := m.redirectURL(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		redirectWithoutCache(w, r, location)
	})
}

func redirectWithoutCache(w http.ResponseWriter, r *http.Request, location string) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.Redirect(w, r, location, http.StatusTemporaryRedirect)
}

func (m *Manager) shouldRedirect(r *http.Request) bool {
	if requestscheme.IsHTTPS(r) {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings.Enabled && m.settings.RedirectHTTP && m.status.Running && m.status.Ready
}

func (m *Manager) redirectURL(r *http.Request) (string, error) {
	m.mu.RLock()
	listen := m.settings.Listen
	listener := m.listener
	m.mu.RUnlock()
	_, httpsPort, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if httpsPort == "0" && listener != nil {
		if _, activePort, splitErr := net.SplitHostPort(listener.Addr().String()); splitErr == nil {
			httpsPort = activePort
		}
	}
	host := r.Host
	hadExplicitPort := false
	if parsedHost, _, splitErr := net.SplitHostPort(r.Host); splitErr == nil {
		host = parsedHost
		hadExplicitPort = true
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	if hadExplicitPort && httpsPort != "443" {
		host = net.JoinHostPort(host, httpsPort)
	} else if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	target := url.URL{Scheme: "https", Host: host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	return target.String(), nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopWatcherLocked()
	server := m.server
	redirectServer := m.redirectServer
	listener := m.listener
	m.server = nil
	m.redirectServer = nil
	m.listener = nil
	m.status.Running = false
	m.status.Ready = false
	m.status.ListenerIPv4 = false
	m.status.ListenerIPv6 = false
	m.status.ListenerProbeDone = true
	m.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	return shutdownHTTPServers(ctx, server, redirectServer)
}

func shutdownServers(server, redirectServer *http.Server, listener net.Listener) {
	if server == nil && redirectServer == nil && listener == nil {
		return
	}
	if listener != nil {
		_ = listener.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = shutdownHTTPServers(ctx, server, redirectServer)
}

func shutdownHTTPServers(ctx context.Context, servers ...*http.Server) error {
	type shutdownResult struct{ err error }
	results := make(chan shutdownResult, len(servers))
	count := 0
	for _, server := range servers {
		if server == nil {
			continue
		}
		count++
		go func(server *http.Server) {
			results <- shutdownResult{err: server.Shutdown(ctx)}
		}(server)
	}
	var firstErr error
	for range count {
		if result := <-results; result.err != nil && firstErr == nil {
			firstErr = result.err
		}
	}
	return firstErr
}

func parseCertificatePair(certPEM, keyPEM []byte) (*tls.Certificate, certificateMetadata, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, certificateMetadata{}, fmt.Errorf("certificate and private key do not match: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, certificateMetadata{}, errors.New("certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, certificateMetadata{}, fmt.Errorf("parse certificate: %w", err)
	}
	if time.Now().After(leaf.NotAfter) {
		return nil, certificateMetadata{}, errors.New("certificate has expired")
	}
	cert.Leaf = leaf
	domains := append([]string(nil), leaf.DNSNames...)
	if leaf.Subject.CommonName != "" {
		domains = append(domains, leaf.Subject.CommonName)
	}
	domains = uniqueStrings(domains)
	sum := sha256.Sum256(leaf.Raw)
	return &cert, certificateMetadata{
		Domains:     domains,
		Issuer:      leaf.Issuer.CommonName,
		ExpiresAt:   leaf.NotAfter.UTC(),
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
