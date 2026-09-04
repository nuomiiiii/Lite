package remote

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
)

func TestAgentRemoteAllowedRequiresProtocolAndLocalSwitch(t *testing.T) {
	if err := AgentRemoteAllowed(models.Client{RemoteProtocol: 2, RemoteControlEnabled: true}); err != nil {
		t.Fatalf("ready agent rejected: %v", err)
	}
	if err := AgentRemoteAllowed(models.Client{RemoteControlEnabled: true}); err != errAgentTooOld {
		t.Fatalf("missing protocol error = %v, want too old", err)
	}
	if err := AgentRemoteAllowed(models.Client{RemoteProtocol: 1, RemoteControlEnabled: true}); err != errAgentTooOld {
		t.Fatalf("protocol 1 error = %v, want too old", err)
	}
	if err := AgentRemoteAllowed(models.Client{RemoteProtocol: 2}); err != errAgentRemoteDisabled {
		t.Fatalf("agent switch off error = %v, want disabled", err)
	}
	if err := AgentRemoteAllowed(models.Client{RemoteProtocol: 2, RemoteControlEnabled: true, RemoteControlProtected: true}); err != nil {
		t.Fatalf("historical remote_control_protected must not block a ready agent: %v", err)
	}
}

func TestRemoteHTTPAndHTTPSAreBothAllowedByPolicy(t *testing.T) {
	_ = tls.VersionTLS13
	loopback := httptest.NewRequest("POST", "http://127.0.0.1:27777/api/admin/client/remote/session", nil)
	loopback.RemoteAddr = "127.0.0.1:5273"
	if loopback.URL.Scheme != "http" {
		t.Fatal("loopback fixture is not HTTP")
	}

	publicHTTP := httptest.NewRequest("POST", "http://monitor.example/api/admin/client/remote/session", nil)
	publicHTTP.Host = "monitor.example"
	publicHTTP.RemoteAddr = "203.0.113.10:443"
	if publicHTTP.TLS != nil {
		t.Fatal("public HTTP fixture unexpectedly has TLS")
	}

	publicHTTPS := httptest.NewRequest("POST", "https://monitor.example/api/admin/client/remote/session", nil)
	publicHTTPS.Host = "monitor.example"
	publicHTTPS.RemoteAddr = "203.0.113.10:443"
	publicHTTPS.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	if publicHTTPS.TLS == nil {
		t.Fatal("public HTTPS fixture is missing TLS")
	}
}
