package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAgentProtocolHeadersAcceptLiteOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	remote, _ := gin.CreateTestContext(httptest.NewRecorder())
	remote.Request = httptest.NewRequest("GET", "/api/clients/remote", nil)
	remote.Request.Header.Set("X-Komari-Remote-Session", "komari-remote")
	remote.Request.Header.Set("X-Komari-Remote-Ticket", "komari-ticket")
	if AgentRemoteSessionHeader(remote) != "" || AgentRemoteTicketHeader(remote) != "" {
		t.Fatal("komari remote headers must be ignored")
	}
	remote.Request.Header.Set(LiteRemoteSessionHeader, "lite-remote")
	remote.Request.Header.Set(LiteRemoteTicketHeader, "lite-ticket")
	if AgentRemoteSessionHeader(remote) != "lite-remote" || AgentRemoteTicketHeader(remote) != "lite-ticket" {
		t.Fatal("lite remote headers should be accepted")
	}
}
