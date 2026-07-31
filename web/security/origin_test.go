package security

import (
	"net/http/httptest"
	"testing"
)

func TestOriginMatchesRequestThroughPrivateReverseProxy(t *testing.T) {
	request := httptest.NewRequest("GET", "http://komari:25774/api/clients", nil)
	request.Host = "komari:25774"
	request.RemoteAddr = "172.17.0.1:41234"
	request.Header.Set("X-Forwarded-Host", "monitor.example.com")

	if !OriginMatchesRequest("https://monitor.example.com", request) {
		t.Fatal("forwarded public host was not accepted from a private reverse proxy")
	}
}

func TestOriginMatchesRequestSupportsStandardForwardedHost(t *testing.T) {
	request := httptest.NewRequest("GET", "http://127.0.0.1:25774/api/clients", nil)
	request.Host = "127.0.0.1:25774"
	request.RemoteAddr = "127.0.0.1:41234"
	request.Header.Set("Forwarded", `for=192.0.2.10;proto=https;host="monitor.example.com"`)

	if !OriginMatchesRequest("https://monitor.example.com", request) {
		t.Fatal("standard forwarded host was not accepted from a loopback proxy")
	}
}

func TestOriginMatchesRequestRejectsSpoofedForwardedHost(t *testing.T) {
	request := httptest.NewRequest("GET", "http://monitor.internal/api/clients", nil)
	request.Host = "monitor.internal"
	request.RemoteAddr = "203.0.113.10:41234"
	request.Header.Set("X-Forwarded-Host", "evil.example")

	if OriginMatchesRequest("https://evil.example", request) {
		t.Fatal("forwarded host from a public client bypassed origin validation")
	}
}

func TestOriginMatchesRequestKeepsDirectSameHostSupport(t *testing.T) {
	request := httptest.NewRequest("GET", "https://monitor.example.com/api/clients", nil)
	request.Host = "monitor.example.com"
	request.RemoteAddr = "203.0.113.10:41234"

	if !OriginMatchesRequest("https://monitor.example.com", request) {
		t.Fatal("direct same-host origin was rejected")
	}
}
