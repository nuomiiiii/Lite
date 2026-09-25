package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const (
	tokenPrefixAccess  = "mcp_"
	tokenPrefixRefresh = "mcr_"
	tokenPrefixCode    = "mcc_"
	pkceS256           = "S256"
	scopeAgentFull     = "agent:full"
	modeFull           = "full"
)

var (
	ErrPKCEInvalid      = errors.New("PKCE S256 is required")
	ErrRedirectInvalid  = errors.New("redirect URI is not allowed")
	ErrResourceInvalid  = errors.New("resource does not match this Lite instance")
	ErrClientMismatch   = errors.New("OAuth client does not match this authorization")
	ErrTokenReuse       = errors.New("refresh token reuse detected")
	ErrAPIKeyForbidden  = errors.New("API keys cannot authorize MCP")
	ErrCookieForbidden  = errors.New("administrator sessions cannot authorize MCP")
	ErrMCPDisabled      = errors.New("MCP is disabled")
	ErrRemoteDisabled   = errors.New("站点未启用远程管理")
	ErrLeaseInactive    = errors.New("MCP authorization is no longer active")
	ErrNodeNotInLease   = errors.New("this node is not in the current MCP authorization")
	ErrAgentUnsupported = errors.New("当前 Agent 不支持 MCP 完整管理，请升级")
	ErrConcurrency      = errors.New("MCP concurrency budget is full")
)

func newOpaqueToken(prefix string) (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	plain := prefix + hex.EncodeToString(raw[:])
	return plain, hashToken(plain), nil
}

func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func requestDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return hashToken(string(encoded))
}

func verifyPKCE(method, challenge, verifier string) error {
	if !strings.EqualFold(strings.TrimSpace(method), pkceS256) {
		return ErrPKCEInvalid
	}
	challenge = strings.TrimSpace(challenge)
	verifier = strings.TrimSpace(verifier)
	if challenge == "" || verifier == "" {
		return ErrPKCEInvalid
	}
	sum := sha256.Sum256([]byte(verifier))
	encoded := base64.RawURLEncoding.EncodeToString(sum[:])
	if encoded != challenge {
		return ErrPKCEInvalid
	}
	return nil
}

func parseRedirectURI(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ErrRedirectInvalid
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Fragment != "" {
		return nil, ErrRedirectInvalid
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return parsed, nil
	case "http":
		host := strings.ToLower(parsed.Hostname())
		if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			return parsed, nil
		}
	}
	return nil, ErrRedirectInvalid
}

func exactRedirectMatch(allowed []string, candidate string) bool {
	normalized, err := parseRedirectURI(candidate)
	if err != nil {
		return false
	}
	want := normalized.String()
	for _, item := range allowed {
		parsed, err := parseRedirectURI(item)
		if err != nil {
			continue
		}
		if parsed.String() == want {
			return true
		}
	}
	return false
}

func splitJSONList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err == nil {
		return compactStrings(items)
	}
	return compactStrings(strings.Split(raw, ","))
}

func compactStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			if _, exists := seen[value]; exists {
				continue
			}
		}
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func encodeJSONList(values []string) string {
	encoded, err := json.Marshal(compactStrings(values))
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func resourceURL(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + "/mcp"
}

func sameResource(got, want string) bool {
	return strings.TrimRight(strings.TrimSpace(got), "/") == strings.TrimRight(strings.TrimSpace(want), "/")
}

func publicBaseURL(scheme, host string) string {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme != "https" {
		scheme = "http"
	}
	host = strings.TrimSpace(host)
	return scheme + "://" + host
}

func requestScheme(https bool) string {
	if https {
		return "https"
	}
	return "http"
}
