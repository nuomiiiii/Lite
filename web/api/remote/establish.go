package remote

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/pkg/rpc"
	"github.com/komari-monitor/komari/web/api"
)

func EstablishAgent(c *gin.Context) {
	session := getSession(agentRemoteSessionID(c))
	principal := api.GetPrincipal(c)
	if session == nil || principal == nil || principal.Type != rpc.PrincipalAgent ||
		principal.ClientUUID != session.UUID || time.Now().After(session.ExpiresAt) {
		api.RespondError(c, http.StatusNotFound, "Remote session not found")
		return
	}
	ticket := c.GetHeader("X-Komari-Remote-Ticket")
	if !session.canAttachAgent(principal.ClientUUID, ticket, time.Now()) {
		api.RespondError(c, http.StatusUnauthorized, "Remote session authorization failed")
		return
	}
	conn, err := api.UpgradeWebSocket(c, api.AllowAgentWebSocket)
	if err != nil {
		deleteSession(session.ID)
		return
	}
	conn.SetReadLimit(remoteReadLimit)
	if !session.attachAgent(principal.ClientUUID, ticket, conn, time.Now()) {
		_ = conn.Close()
		return
	}
	session.forwardOnce.Do(func() {
		go forwardSession(session)
	})
}

func agentRemoteSessionID(c *gin.Context) string {
	return c.GetHeader("X-Komari-Remote-Session")
}
