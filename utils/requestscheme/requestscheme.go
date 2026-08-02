package requestscheme

import (
	"net"
	"net/http"
	"strings"
)

// IsHTTPS reports whether the request arrived over TLS or through a trusted
// local reverse proxy that explicitly identified the original request as HTTPS.
func IsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return IsForwardedHTTPS(r)
}

// IsForwardedHTTPS reports whether a trusted local proxy marked the original
// request as HTTPS, independently of the proxy-to-Komari transport.
func IsForwardedHTTPS(r *http.Request) bool {
	return r != nil && isTrustedForwarder(r.RemoteAddr) && forwardedHTTPS(r.Header)
}

func isTrustedForwarder(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func forwardedHTTPS(header http.Header) bool {
	if strings.EqualFold(lastForwardedValue(header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	if strings.EqualFold(lastForwardedValue(header.Get("X-Forwarded-Protocol")), "https") {
		return true
	}
	if strings.EqualFold(lastForwardedValue(header.Get("X-Url-Scheme")), "https") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(header.Get("X-Forwarded-Ssl")), "on") {
		return true
	}
	for _, forwarded := range strings.Split(header.Get("Forwarded"), ",") {
		for _, part := range strings.Split(forwarded, ";") {
			key, value, ok := strings.Cut(part, "=")
			if ok && strings.EqualFold(strings.TrimSpace(key), "proto") &&
				strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"`), "https") {
				return true
			}
		}
	}
	return false
}

func lastForwardedValue(value string) string {
	parts := strings.Split(value, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if candidate := strings.TrimSpace(parts[i]); candidate != "" {
			return candidate
		}
	}
	return ""
}
