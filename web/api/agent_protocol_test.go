package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAgentProtocolHeadersAcceptLiteAndKomari(t *testing.T) {
	gin.SetMode(gin.TestMode)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/clients/terminal", nil)
	if AgentTerminalSessionHeader(context) != "" {
		t.Fatal("empty headers returned a session id")
	}

	context.Request.Header.Set(KomariTerminalSessionHeader, "komari-session")
	if got := AgentTerminalSessionHeader(context); got != "komari-session" {
		t.Fatalf("komari terminal header = %q", got)
	}

	context.Request.Header.Set(LiteTerminalSessionHeader, "lite-session")
	if got := AgentTerminalSessionHeader(context); got != "lite-session" {
		t.Fatalf("lite terminal header should win, got %q", got)
	}

	remote, _ := gin.CreateTestContext(httptest.NewRecorder())
	remote.Request = httptest.NewRequest("GET", "/api/clients/remote", nil)
	remote.Request.Header.Set(KomariRemoteSessionHeader, "komari-remote")
	remote.Request.Header.Set(KomariRemoteTicketHeader, "komari-ticket")
	if AgentRemoteSessionHeader(remote) != "komari-remote" || AgentRemoteTicketHeader(remote) != "komari-ticket" {
		t.Fatal("komari remote headers were not accepted")
	}
	remote.Request.Header.Set(LiteRemoteSessionHeader, "lite-remote")
	remote.Request.Header.Set(LiteRemoteTicketHeader, "lite-ticket")
	if AgentRemoteSessionHeader(remote) != "lite-remote" || AgentRemoteTicketHeader(remote) != "lite-ticket" {
		t.Fatal("lite remote headers should win")
	}
}
