package remote

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/pkg/rpc"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	"github.com/komari-monitor/komari/utils"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
	"github.com/komari-monitor/komari/web/api"
)

func CreateSession(c *gin.Context) {
	principal := api.GetPrincipal(c)
	if principal == nil || principal.Type != rpc.PrincipalUser {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	uuid := c.Param("uuid")
	client, err := clients.GetClientByUUID(uuid)
	if err != nil {
		api.RespondError(c, http.StatusNotFound, "Client not found")
		return
	}
	if client.RemoteControlProtected {
		api.RespondError(c, http.StatusForbidden, "Remote control is disabled for the Komari Server node")
		return
	}
	if !agent_runtime.IsAgentOnline(uuid) {
		api.RespondError(c, http.StatusConflict, "Client is offline")
		return
	}
	loginSession, _ := c.Cookie("session_token")
	if err := verifyRemoteAccess(c, loginSession); err != nil {
		api.RespondError(c, http.StatusUnauthorized, err.Error())
		return
	}

	now := time.Now()
	session := &remoteSession{
		ID:            utils.GenerateRandomString(32),
		UUID:          uuid,
		UserUUID:      principal.UserUUID,
		LoginSession:  loginSession,
		RequesterIP:   c.ClientIP(),
		BrowserTicket: utils.GenerateRandomString(32),
		AgentTicket:   utils.GenerateRandomString(32),
		CreatedAt:     now,
		ExpiresAt:     now.Add(pendingSessionTTL),
	}
	if session.ID == "" || session.BrowserTicket == "" || session.AgentTicket == "" {
		api.RespondError(c, http.StatusInternalServerError, "Failed to create secure remote session")
		return
	}
	if err := putSession(session); err != nil {
		api.RespondError(c, http.StatusTooManyRequests, err.Error())
		return
	}
	auditlog.Log(session.RequesterIP, session.UserUUID, "request remote session, client:"+uuid, "terminal")
	time.AfterFunc(pendingSessionTTL, func() {
		session.mu.Lock()
		pending := session.StartedAt.IsZero()
		session.mu.Unlock()
		if pending {
			deleteSession(session.ID)
		}
	})
	api.RespondSuccess(c, gin.H{
		"session_id":     session.ID,
		"browser_ticket": session.BrowserTicket,
		"expires_at":     session.ExpiresAt.UTC(),
	})
}

// Authorize verifies the remote-management step-up before the browser creates
// a terminal tab. This keeps the 2FA prompt independent from xterm startup and
// avoids creating a pending remote session solely to discover that 2FA is due.
func Authorize(c *gin.Context) {
	principal := api.GetPrincipal(c)
	if principal == nil || principal.Type != rpc.PrincipalUser {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	loginSession, _ := c.Cookie("session_token")
	if err := verifyRemoteAccess(c, loginSession); err != nil {
		api.RespondError(c, http.StatusUnauthorized, err.Error())
		return
	}
	api.RespondSuccess(c, gin.H{"authorized": true})
}

func verifyRemoteAccess(c *gin.Context, loginSession string) error {
	if hasFreshStepUp(loginSession) {
		return nil
	}
	if err := api.VerifySensitive2FA(c); err != nil {
		return err
	}
	rememberStepUp(loginSession)
	return nil
}

func ConnectBrowser(c *gin.Context) {
	session := getSession(c.Query("id"))
	principal := api.GetPrincipal(c)
	loginSession, _ := c.Cookie("session_token")
	if session == nil || principal == nil || principal.Type != rpc.PrincipalUser ||
		principal.UserUUID != session.UserUUID || loginSession != session.LoginSession ||
		c.Param("uuid") != session.UUID || time.Now().After(session.ExpiresAt) {
		api.RespondError(c, http.StatusNotFound, "Remote session not found")
		return
	}
	conn, err := api.UpgradeWebSocket(c, api.RequireSameOriginWebSocket)
	if err != nil {
		return
	}
	conn.SetReadLimit(remoteReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var auth struct {
		Type   string `json:"type"`
		Ticket string `json:"ticket"`
	}
	if err := conn.ReadJSON(&auth); err != nil || auth.Type != "auth" {
		_ = conn.Close()
		return
	}

	valid := session.attachBrowser(auth.Ticket, conn, time.Now())
	if !valid {
		_ = conn.WriteJSON(gin.H{"type": "remote.error", "message": "Remote session authorization failed"})
		_ = conn.Close()
		return
	}
	agentTicket := session.pendingAgentTicket()
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.WriteJSON(gin.H{"type": "remote.status", "status": "waiting"})
	params := v2.RemoteRequestParams{RequestID: session.ID, Ticket: agentTicket}
	if !dispatchRemoteRequest(session.UUID, params) {
		_ = conn.WriteJSON(gin.H{"type": "remote.error", "message": "Client is offline"})
		deleteSession(session.ID)
	}
}

func dispatchRemoteRequest(uuid string, params v2.RemoteRequestParams) bool {
	if conn := agent_runtime.GetConnectedClients()[uuid]; conn != nil {
		var payload any = gin.H{
			"message":       "remote",
			"request_id":    params.RequestID,
			"remote_ticket": params.Ticket,
		}
		if agent_runtime.IsV2Client(uuid) {
			payload = v2.Request{JSONRPC: v2.Version, Method: v2.MethodAgentRemote, Params: params}
		}
		return conn.WriteJSON(payload) == nil
	}
	if !agent_runtime.IsV2Client(uuid) {
		return false
	}
	agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentRemote, params)
	return true
}
