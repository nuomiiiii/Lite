package security

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/nuomiiiii/lite/pkg/config"
)

func SplitAllowlist(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	entries := make([]string, 0, len(parts))
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func OriginMatchesHost(origin, host string) bool {
	_, originHost, ok := normalizeOrigin(origin)
	return ok && strings.EqualFold(originHost, host)
}

// OriginMatchesRequest accepts the direct Host and, only behind a local or
// private reverse proxy, the original host forwarded by that proxy.
func OriginMatchesRequest(origin string, r *http.Request) bool {
	if OriginMatchesHost(origin, r.Host) {
		return true
	}
	if !isPrivateProxy(r.RemoteAddr) {
		return false
	}
	for _, host := range forwardedHosts(r) {
		if OriginMatchesHost(origin, host) {
			return true
		}
	}
	return false
}

func OriginInAllowlist(origin, rawAllowlist string) bool {
	normalizedOrigin, originHost, ok := normalizeOrigin(origin)
	if !ok {
		return false
	}
	for _, entry := range SplitAllowlist(rawAllowlist) {
		if entry == "*" {
			return true
		}
		if strings.Contains(entry, "://") {
			normalizedEntry, _, ok := normalizeOrigin(entry)
			if ok && strings.EqualFold(normalizedEntry, normalizedOrigin) {
				return true
			}
			continue
		}
		if strings.EqualFold(entry, originHost) {
			return true
		}
	}
	return false
}

func IsAPIKeyRequest(r *http.Request) bool {
	apiKeyConfig, err := config.GetAs[string](config.ApiKeyKey, "")
	if err != nil || apiKeyConfig == "" || len(apiKeyConfig) < 12 {
		return false
	}
	return r.Header.Get("Authorization") == "Bearer "+apiKeyConfig
}

func IsAuthorizationPreflight(r *http.Request) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		if strings.EqualFold(strings.TrimSpace(header), "authorization") {
			return true
		}
	}
	return false
}

func normalizeOrigin(raw string) (string, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Host)
	return strings.ToLower(parsed.Scheme) + "://" + host, host, true
}

func isPrivateProxy(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func forwardedHosts(r *http.Request) []string {
	hosts := make([]string, 0, 2)
	for _, value := range strings.Split(r.Header.Get("X-Forwarded-Host"), ",") {
		if host := cleanForwardedHost(value); host != "" {
			hosts = append(hosts, host)
		}
	}
	for _, forwarded := range strings.Split(r.Header.Get("Forwarded"), ",") {
		for _, parameter := range strings.Split(forwarded, ";") {
			key, value, ok := strings.Cut(parameter, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "host") {
				continue
			}
			if host := cleanForwardedHost(value); host != "" {
				hosts = append(hosts, host)
			}
		}
	}
	return hosts
}

func cleanForwardedHost(value string) string {
	host := strings.Trim(strings.TrimSpace(value), `"`)
	if host == "" || strings.ContainsAny(host, "/\\@") {
		return ""
	}
	return host
}
