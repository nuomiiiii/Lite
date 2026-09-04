package security

import (
	"net/http/httptest"
	"testing"
)

func TestOriginMatchesRequestThroughPrivateReverseProxy(t *testing.T) {
	request := httptest.NewRequest("GET", "http://lite:27777/api/clients", nil)
	request.Host = "lite:27777"
	request.RemoteAddr = "172.17.0.1:41234"
	request.Header.Set("X-Forwarded-Host", "monitor.example.com")

	if !OriginMatchesRequest("https://monitor.example.com", request) {
		t.Fatal("forwarded public host was not accepted from a private reverse proxy")
	}
}

func TestOriginMatchesRequestSupportsStandardForwardedHost(t *testing.T) {
	request := httptest.NewRequest("GET", "http://127.0.0.1:27777/api/clients", nil)
	request.Host = "127.0.0.1:27777"
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

func TestRemoteOriginAllowedRejectsEmptyNullAndCrossOrigin(t *testing.T) {
	request := httptest.NewRequest("GET", "https://monitor.example/api/admin/client/remote", nil)
	request.Host = "monitor.example"
	request.RemoteAddr = "203.0.113.10:443"

	if RemoteOriginAllowed(request) {
		t.Fatal("missing origin was accepted")
	}
	request.Header.Set("Origin", "null")
	if RemoteOriginAllowed(request) {
		t.Fatal("opaque origin was accepted")
	}
	request.Header.Set("Origin", "https://attacker.example")
	if RemoteOriginAllowed(request) {
		t.Fatal("cross-origin remote request was accepted")
	}
	request.Header.Set("Origin", "https://monitor.example")
	if !RemoteOriginAllowed(request) {
		t.Fatal("same-origin remote request was rejected")
	}
}

func TestRemoteOriginAllowedAllowsLoopbackViteAndRejectsSpoofedVite(t *testing.T) {
	loopback := httptest.NewRequest("GET", "http://127.0.0.1:27777/api/admin/client/remote", nil)
	loopback.Host = "127.0.0.1:27777"
	loopback.RemoteAddr = "127.0.0.1:41234"
	loopback.Header.Set("Origin", "http://127.0.0.1:5273")
	if !RemoteOriginAllowed(loopback) {
		t.Fatal("loopback Vite origin was rejected")
	}

	public := httptest.NewRequest("GET", "http://127.0.0.1:27777/api/admin/client/remote", nil)
	public.Host = "127.0.0.1:27777"
	public.RemoteAddr = "203.0.113.10:41234"
	public.Header.Set("Origin", "http://127.0.0.1:5273")
	if RemoteOriginAllowed(public) {
		t.Fatal("non-loopback client used a spoofed Vite origin")
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
