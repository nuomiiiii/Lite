package remote

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/pkg/rpc"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/remotectl"
)

type browserAuthorization struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Ticket    string `json:"ticket"`
}

type cancelSessionRequest struct {
	SessionID string `json:"session_id" binding:"required"`
}

type revokeGrantRequest struct {
	Grant string `json:"grant"`
}

func (authorization browserAuthorization) valid() bool {
	return authorization.Type == "auth" && authorization.SessionID != "" && authorization.Ticket != ""
}

func CreateSession(c *gin.Context) {
	noStore(c)
	if !rejectIfRemoteOriginDenied(c) {
		return
	}
	principal := api.GetPrincipal(c)
	if principal == nil || principal.Type != rpc.PrincipalUser {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	loginSession, _ := c.Cookie("session_token")
	if loginSession == "" {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	var request struct {
		UUID   string `json:"uuid" binding:"required"`
		Grant  string `json:"grant"`
		PageID string `json:"page_id"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Client UUID is required")
		return
	}
	if err := remotectl.ConsumeGrant(request.Grant, principal.UserUUID, loginSession, remotectl.ScopeRemote, request.PageID); err != nil {
		respondGrantError(c, err)
		return
	}
	client, err := clients.GetClientByUUID(request.UUID)
	if err != nil {
		api.RespondError(c, http.StatusNotFound, "Client not found")
		return
	}
	if !agent_runtime.IsAgentOnline(request.UUID) {
		api.RespondError(c, http.StatusConflict, "Client is offline")
		return
	}
	if err := ensureRemoteAllowed(client); err != nil {
		api.RespondError(c, remotePolicyStatus(err), err.Error())
		return
	}

	now := time.Now()
	session := &remoteSession{
		ID:            utils.GenerateRandomString(32),
		UUID:          request.UUID,
		UserUUID:      principal.UserUUID,
		LoginSession:  accounts.SessionLookupKey(loginSession),
		RequesterIP:   c.ClientIP(),
		BrowserTicket: utils.GenerateRandomString(32),
		AgentTicket:   utils.GenerateRandomString(32),
		CreatedAt:     now,
		ExpiresAt:     now.Add(pendingSessionTTL),
		LastActivity:  now,
	}
	if session.ID == "" || session.BrowserTicket == "" || session.AgentTicket == "" {
		api.RespondError(c, http.StatusInternalServerError, "Failed to create secure remote session")
		return
	}
	if err := putSessionUnderDeliveryGate(session); err != nil {
		if errors.Is(err, errRemoteManagementDisabled) {
			api.RespondError(c, remotePolicyStatus(err), err.Error())
			return
		}
		if errors.Is(err, errRemoteSessionLimit) || errors.Is(err, errLoginSessionLimit) {
			api.RespondError(c, http.StatusTooManyRequests, "远程会话数量已满，请关闭不用的终端后重试")
		} else {
			api.RespondError(c, http.StatusConflict, err.Error())
		}
		return
	}
	auditlog.Log(session.RequesterIP, session.UserUUID, "request remote session, client:"+request.UUID, "terminal")
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

func Authorize(c *gin.Context) {
	noStore(c)
	if !rejectIfRemoteOriginDenied(c) {
		return
	}
	principal := api.GetPrincipal(c)
	if principal == nil || principal.IsAPIKey || principal.Type == rpc.PrincipalAPIKey {
		api.RespondError(c, http.StatusForbidden, remotectl.ErrAPIKeyForbidden.Error())
		return
	}
	if principal.Type != rpc.PrincipalUser {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	loginSession, _ := c.Cookie("session_token")
	if loginSession == "" {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	var request struct {
		Password string `json:"password"`
		OTP      string `json:"otp"`
		TwoFA    string `json:"2fa_code"`
		Scope    string `json:"scope"`
		PageID   string `json:"page_id"`
	}
	_ = c.ShouldBindJSON(&request)
	otp := request.OTP
	if otp == "" {
		otp = request.TwoFA
	}
	scope := request.Scope
	if scope == "" {
		scope = remotectl.ScopeRemote
	}
	if err := remotectl.Reauthorize(principal.UserUUID, request.Password, otp, c.ClientIP()); err != nil {
		respondGrantError(c, err)
		return
	}
	grant, expires, err := remotectl.IssueGrant(principal.UserUUID, loginSession, scope, request.PageID)
	if err != nil {
		respondGrantError(c, err)
		return
	}
	api.RespondSuccess(c, gin.H{
		"grant":      grant,
		"expires_at": expires.UTC(),
		"scope":      scope,
	})
}

func RevokeGrant(c *gin.Context) {
	noStore(c)
	if !rejectIfRemoteOriginDenied(c) {
		return
	}
	principal := api.GetPrincipal(c)
	if principal == nil || principal.Type != rpc.PrincipalUser {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	var request revokeGrantRequest
	_ = c.ShouldBindJSON(&request)
	remotectl.RevokeGrant(request.Grant)
	api.RespondSuccess(c, gin.H{"revoked": true})
}

func CancelSession(c *gin.Context) {
	noStore(c)
	if !rejectIfRemoteOriginDenied(c) {
		return
	}
	principal := api.GetPrincipal(c)
	if principal == nil || principal.Type != rpc.PrincipalUser {
		api.RespondError(c, http.StatusForbidden, "Remote control requires an administrator session")
		return
	}
	var request cancelSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Remote session ID is required")
		return
	}
	loginSession, _ := c.Cookie("session_token")
	if loginSession == "" || !deleteOwnedSession(request.SessionID, principal.UserUUID, loginSession) {
		api.RespondError(c, http.StatusNotFound, "Remote session not found")
		return
	}
	api.RespondSuccess(c, gin.H{"released": true})
}

func ConnectBrowser(c *gin.Context) {
	if !rejectIfRemoteOriginDenied(c) {
		return
	}
	principal := api.GetPrincipal(c)
	loginSession, _ := c.Cookie("session_token")
	if principal == nil || principal.Type != rpc.PrincipalUser || loginSession == "" {
		api.RespondError(c, http.StatusNotFound, "Remote session not found")
		return
	}
	conn, err := api.UpgradeWebSocket(c, api.RequireRemoteBrowserOrigin)
	if err != nil {
		return
	}
	conn.SetReadLimit(remoteReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var auth browserAuthorization
	if err := conn.ReadJSON(&auth); err != nil || !auth.valid() {
		_ = conn.Close()
		return
	}
	session := getSession(auth.SessionID)
	if session == nil || principal.UserUUID != session.UserUUID ||
		accounts.SessionLookupKey(loginSession) != session.LoginSession ||
		time.Now().After(session.ExpiresAt) {
		_ = conn.WriteJSON(gin.H{"type": "remote.error", "message": "Remote session authorization failed"})
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
	if err := dispatchRemoteRequest(session.UUID, params); err != nil {
		_ = conn.WriteJSON(gin.H{"type": "remote.error", "message": err.Error()})
		deleteSession(session.ID)
	}
}

func dispatchRemoteRequest(uuid string, params v2.RemoteRequestParams) error {
	var dispatchErr error
	accepted := agent_runtime.GuardRemoteDelivery(RemoteManagementEnabled, func() {
		dispatchErr = enqueueOrSendRemoteRequest(uuid, params)
	})
	if !accepted {
		return errRemoteManagementDisabled
	}
	return dispatchErr
}

func enqueueOrSendRemoteRequest(uuid string, params v2.RemoteRequestParams) error {
	if conn := agent_runtime.GetConnectedClient(uuid); conn != nil {
		payload := v2.Request{JSONRPC: v2.Version, Method: v2.MethodAgentRemote, Params: params}
		if conn.WriteJSON(payload) == nil {
			return nil
		}
	}
	if !agent_runtime.IsAgentOnline(uuid) {
		return errRemoteClientOffline
	}
	event := agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentRemote, params)
	if event.ID == "" {
		return errRemoteQueueFull
	}
	return nil
}

func putSessionUnderDeliveryGate(session *remoteSession) error {
	var putErr error
	accepted := agent_runtime.GuardRemoteDelivery(RemoteManagementEnabled, func() {
		putErr = putSession(session)
	})
	if !accepted {
		return errRemoteManagementDisabled
	}
	return putErr
}

func noStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
}

func respondGrantError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, remotectl.ErrRateLimited), accounts.IsPasswordBusy(err):
		api.RespondError(c, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, remotectl.ErrAPIKeyForbidden), errors.Is(err, remotectl.ErrSSOReauth):
		api.RespondError(c, http.StatusForbidden, err.Error())
	default:
		api.RespondError(c, http.StatusUnauthorized, err.Error())
	}
}
