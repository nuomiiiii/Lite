package themehttp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	MaxArchive    int64 = 128 << 20
	MaxCatalog    int64 = 2 << 20
	MaxPreview    int64 = 8 << 20
	MaxGitHubJSON int64 = 2 << 20

	maxRedirects = 10
)

type LookupNetIPFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Client struct {
	Timeout         time.Duration
	LookupNetIP     LookupNetIPFunc
	DialContext     DialContextFunc
	TLSClientConfig *tls.Config

	httpClient *http.Client
}

var Default = New()

func New() *Client {
	return NewClient(Client{})
}

func NewClient(c Client) *Client {
	out := c
	if out.Timeout <= 0 {
		out.Timeout = 45 * time.Second
	}
	out.initHTTP()
	return &out
}

func DownloadBytes(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	return Default.DownloadBytes(ctx, rawURL, maxBytes)
}

func DownloadFile(ctx context.Context, rawURL string, maxBytes int64, prefix string) (string, []byte, error) {
	return Default.DownloadFile(ctx, rawURL, maxBytes, prefix)
}

func (c *Client) initHTTP() {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	dial := c.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: -1}).DialContext
	}
	lookup := c.LookupNetIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	c.LookupNetIP = lookup
	c.DialContext = dial

	inner := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return c.dialPinned(ctx, network, address)
		},
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSClientConfig:       c.TLSClientConfig,
	}
	c.httpClient = &http.Client{
		Transport: &pinningTransport{client: c, inner: inner},
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return ErrTooManyRedirects
			}
			if err := validateParsedURL(req.URL); err != nil {
				return err
			}
			return nil
		},
	}
}

func (c *Client) DownloadBytes(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	resp, err := c.get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp, maxBytes); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, mapRequestError(err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	if len(data) == 0 {
		return nil, ErrEmpty
	}
	return data, nil
}

func (c *Client) DownloadFile(ctx context.Context, rawURL string, maxBytes int64, prefix string) (string, []byte, error) {
	resp, err := c.get(ctx, rawURL)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if err := checkResponse(resp, maxBytes); err != nil {
		return "", nil, err
	}
	if prefix == "" {
		prefix = "lite-theme"
	}
	tmp, err := os.CreateTemp("", prefix+"-*.part")
	if err != nil {
		return "", nil, ErrTempFile
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return "", nil, ErrTempFile
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", nil, ErrTempFile
	}
	if written > maxBytes {
		return "", nil, ErrTooLarge
	}
	if written == 0 {
		return "", nil, ErrEmpty
	}
	if err := tmp.Close(); err != nil {
		return "", nil, ErrTempFile
	}
	cleanup = false
	return tmpPath, hasher.Sum(nil), nil
}

func (c *Client) get(ctx context.Context, rawURL string) (*http.Response, error) {
	if _, err := parseRequestURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrInvalidURL
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, mapRequestError(err)
	}
	return resp, nil
}

func checkResponse(resp *http.Response, maxBytes int64) error {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w: %d", ErrHTTPStatus, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return ErrTooLarge
	}
	return nil
}

func parseRequestURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, ErrInvalidURL
	}
	return parsed, validateParsedURL(parsed)
}

func validateParsedURL(parsed *url.URL) error {
	if parsed == nil {
		return ErrInvalidURL
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		if scheme == "" {
			return ErrInvalidURL
		}
		return ErrUnsupportedScheme
	}
	if parsed.User != nil {
		return ErrInvalidURL
	}
	host := parsed.Hostname()
	if host == "" {
		return ErrInvalidURL
	}
	port := parsed.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return ErrInvalidURL
		}
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return ErrInvalidURL
	}
	if addr, ok := parseIPLiteral(host); ok && blockedAddr(addr) {
		return ErrPrivateAddress
	}
	return nil
}

type pinnedAddrsKey struct{}

type pinningTransport struct {
	client *Client
	inner  *http.Transport
}

func withPinnedAddrs(ctx context.Context, addrs []netip.Addr) context.Context {
	return context.WithValue(ctx, pinnedAddrsKey{}, addrs)
}

func pinnedAddrs(ctx context.Context) ([]netip.Addr, bool) {
	addrs, ok := ctx.Value(pinnedAddrsKey{}).([]netip.Addr)
	return addrs, ok && len(addrs) > 0
}

func (t *pinningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateParsedURL(req.URL); err != nil {
		return nil, err
	}
	addrs, err := t.client.resolve(req.Context(), req.URL.Hostname())
	if err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(req.Clone(withPinnedAddrs(req.Context(), addrs)))
}

func (c *Client) dialPinned(ctx context.Context, network, address string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalidURL
	}
	addrs, ok := pinnedAddrs(ctx)
	if !ok {
		return nil, ErrDNS
	}
	var last error
	for _, addr := range addrs {
		if !networkOK(network, addr) {
			continue
		}
		conn, err := c.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
		if err == nil {
			return conn, nil
		}
		last = mapRequestError(err)
	}
	if last == nil {
		return nil, ErrDNS
	}
	return nil, last
}

func (c *Client) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, ok := parseIPLiteral(host); ok {
		if blockedAddr(addr) {
			return nil, ErrPrivateAddress
		}
		return []netip.Addr{addr}, nil
	}
	addrs, err := c.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, ErrDNS
	}
	if len(addrs) == 0 {
		return nil, ErrDNS
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if !addr.IsValid() {
			return nil, ErrPrivateAddress
		}
		addr = addr.Unmap()
		if blockedAddr(addr) {
			return nil, ErrPrivateAddress
		}
		out = append(out, addr)
	}
	if len(out) == 0 {
		return nil, ErrDNS
	}
	return out, nil
}

func networkOK(network string, addr netip.Addr) bool {
	switch network {
	case "tcp4", "udp4":
		return addr.Is4()
	case "tcp6", "udp6":
		return addr.Is6()
	default:
		return true
	}
}
