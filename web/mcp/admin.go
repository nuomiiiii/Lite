package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/api/remote"
	"github.com/nuomiiiii/lite/web/passkey"
	"github.com/nuomiiiii/lite/web/remotectl"
	"github.com/nuomiiiii/lite/web/security"
)

func RegisterAdmin(g gin.IRouter) {
	g.GET("/mcp/settings", getSettings)
	g.POST("/mcp/settings", saveSettings)
	g.GET("/mcp/authorization-requests", listAuthorizationRequests)
	g.GET("/mcp/authorization-requests/:id", getAuthorizationRequest)
	g.POST("/mcp/authorization-requests/:id/approve", approveAuthorization)
	g.POST("/mcp/authorization-requests/:id/deny", denyAuthorization)
	g.GET("/mcp/leases", listLeases)
	g.GET("/mcp/leases/:id", getLease)
	g.POST("/mcp/leases/:id/revoke", revokeLease)
	g.POST("/mcp/leases/revoke-all", revokeAllLeases)
	g.GET("/mcp/operations", listOperations)
	g.POST("/mcp/history/purge", purgeHistory)
}

func requireAdminSession(c *gin.Context) (*rpc.Principal, string, bool) {
	if !security.RemoteOriginAllowed(c.Request) {
		api.RespondError(c, http.StatusForbidden, "Remote origin is not allowed")
		return nil, "", false
	}
	principal := api.GetPrincipal(c)
	if principal == nil || principal.IsAPIKey || principal.Type == rpc.PrincipalAPIKey {
		api.RespondError(c, http.StatusForbidden, remotectl.ErrAPIKeyForbidden.Error())
		return nil, "", false
	}
	if principal.Type != rpc.PrincipalUser {
		api.RespondError(c, http.StatusForbidden, "MCP administration requires an administrator session")
		return nil, "", false
	}
	loginSession, _ := c.Cookie("session_token")
	if loginSession == "" {
		api.RespondError(c, http.StatusForbidden, "MCP administration requires an administrator session")
		return nil, "", false
	}
	return principal, loginSession, true
}

func getSettings(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	settings := siteDurationSettings()
	api.RespondSuccess(c, gin.H{
		"allow_mcp":                    mcpEnabled(),
		"allow_remote_management":      remote.RemoteManagementEnabled(),
		"mcp_default_duration_minutes": settings.DefaultMinutes,
		"mcp_max_duration_minutes":     settings.MaxMinutes,
		"mcp_max_concurrency":          settings.MaxConcurrency,
		"endpoint":                     instanceResource(c),
		"hard_max_duration_minutes":    HardMaxDurationMinutes,
		"min_duration_minutes":         MinDurationMinutes,
	})
}

func saveSettings(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	var body struct {
		AllowMCP       *bool `json:"allow_mcp"`
		DefaultMinutes any   `json:"mcp_default_duration_minutes"`
		MaxMinutes     any   `json:"mcp_max_duration_minutes"`
		MaxConcurrency any   `json:"mcp_max_concurrency"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		api.RespondError(c, http.StatusBadRequest, "invalid settings")
		return
	}
	current := siteDurationSettings()
	defaultMinutes := current.DefaultMinutes
	maxMinutes := current.MaxMinutes
	concurrency := current.MaxConcurrency
	if body.DefaultMinutes != nil {
		parsed, err := ParseDurationMinutes(body.DefaultMinutes)
		if err != nil {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		defaultMinutes = parsed
	}
	if body.MaxMinutes != nil {
		parsed, err := ParseDurationMinutes(body.MaxMinutes)
		if err != nil {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		maxMinutes = parsed
	}
	if body.MaxConcurrency != nil {
		switch typed := body.MaxConcurrency.(type) {
		case float64:
			if typed != float64(int(typed)) {
				api.RespondError(c, http.StatusBadRequest, "MCP concurrency must be between 1 and 16")
				return
			}
			concurrency = int(typed)
		case int:
			concurrency = typed
		case string:
			parsed, err := ParseDurationMinutes(typed)
			if err != nil {
				api.RespondError(c, http.StatusBadRequest, "MCP concurrency must be between 1 and 16")
				return
			}
			concurrency = parsed
		default:
			api.RespondError(c, http.StatusBadRequest, "MCP concurrency must be between 1 and 16")
			return
		}
		if concurrency < 1 || concurrency > MaxConcurrencyCap {
			api.RespondError(c, http.StatusBadRequest, "MCP concurrency must be between 1 and 16")
			return
		}
	}
	normalized, err := NormalizeSettings(defaultMinutes, maxMinutes, concurrency)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "default duration must be between 1 minute and the site maximum")
		return
	}
	previousMCP := mcpEnabled()
	updates := map[string]any{
		config.MCPDefaultDurationMinKey: normalized.DefaultMinutes,
		config.MCPMaxDurationMinKey:     normalized.MaxMinutes,
		config.MCPMaxConcurrencyKey:     normalized.MaxConcurrency,
	}
	if body.AllowMCP != nil {
		updates[config.AllowMCPKey] = *body.AllowMCP
	}
	if err := config.SetMany(updates); err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if body.AllowMCP != nil && previousMCP && !*body.AllowMCP {
		DrainMCPDelivery()
	}
	getSettings(c)
}

func listAuthorizationRequests(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	rows, err := pendingAuthorizationRequests(time.Now().UTC(), 20)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, req := range rows {
		items = append(items, authorizationRequestJSON(req))
	}
	api.RespondSuccess(c, gin.H{"requests": items})
}

func pendingAuthorizationRequests(now time.Time, limit int) ([]models.MCPAuthorizationRequest, error) {
	var rows []models.MCPAuthorizationRequest
	if err := database().Where("status = ?", statusPending).
		Order("created_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	fresh := make([]models.MCPAuthorizationRequest, 0, len(rows))
	for _, req := range rows {
		if req.ExpiresAt.After(now) {
			fresh = append(fresh, req)
		}
	}
	out := uniquePendingAuthorizationRequests(fresh)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func uniquePendingAuthorizationRequests(rows []models.MCPAuthorizationRequest) []models.MCPAuthorizationRequest {
	seen := make(map[string]struct{}, len(rows))
	out := make([]models.MCPAuthorizationRequest, 0, len(rows))
	for _, req := range rows {
		key := strings.Join([]string{
			req.ClientID,
			req.RedirectURI,
			req.State,
			req.CodeChallenge,
			req.CodeChallengeMethod,
			req.Resource,
			req.Scope,
		}, "\x1f")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, req)
	}
	return out
}

func getAuthorizationRequest(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	req, err := loadAuthRequest(c.Param("id"))
	if err != nil {
		api.RespondError(c, http.StatusNotFound, "authorization request not found")
		return
	}
	api.RespondSuccess(c, authorizationRequestJSON(req))
}

func authorizationRequestJSON(req models.MCPAuthorizationRequest) gin.H {
	client, _ := loadClient(req.ClientID)
	return gin.H{
		"id":           req.ID,
		"status":       req.Status,
		"client_id":    req.ClientID,
		"client_name":  client.ClientName,
		"redirect_uri": req.RedirectURI,
		"scope":        req.Scope,
		"resource":     req.Resource,
		"ready":        authorizationRequestReady(req),
		"expires_at":   req.ExpiresAt.UTC(),
	}
}

func approveAuthorization(c *gin.Context) {
	principal, loginSession, ok := requireAdminSession(c)
	if !ok {
		return
	}
	req, err := loadAuthRequest(c.Param("id"))
	if err != nil || req.Status != statusPending || !req.ExpiresAt.After(time.Now().UTC()) {
		api.RespondError(c, http.StatusNotFound, "authorization request not found")
		return
	}
	if !authorizationRequestReady(req) {
		api.RespondError(c, http.StatusConflict, "authorization request is not ready")
		return
	}
	var body struct {
		Password        string          `json:"password"`
		OTP             string          `json:"otp"`
		TwoFA           string          `json:"2fa_code"`
		CeremonyID      string          `json:"ceremony_id"`
		Credential      json.RawMessage `json:"credential"`
		TargetUUIDs     []string        `json:"target_uuids"`
		DurationMinutes any             `json:"duration_minutes"`
		Note            string          `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		api.RespondError(c, http.StatusBadRequest, "invalid authorization request")
		return
	}
	otp := body.OTP
	if otp == "" {
		otp = body.TwoFA
	}
	var reauthErr error
	if len(body.Credential) > 0 || body.CeremonyID != "" {
		reauthErr = remotectl.ReauthorizePasskey(principal.UserUUID, c.ClientIP(), func() error {
			return passkey.FinishUserAssertion(c, principal.UserUUID, body.CeremonyID, body.Credential)
		})
	} else {
		reauthErr = remotectl.Reauthorize(principal.UserUUID, body.Password, otp, c.ClientIP())
	}
	if reauthErr != nil {
		status := http.StatusForbidden
		if remotectl.IsRateLimited(reauthErr) {
			status = http.StatusTooManyRequests
		}
		api.RespondError(c, status, reauthErr.Error())
		return
	}
	if !mcpEnabled() || !remote.RemoteManagementEnabled() {
		api.RespondError(c, http.StatusConflict, ErrMCPDisabled.Error())
		return
	}
	settings := siteDurationSettings()
	minutes, err := ParseDurationMinutes(body.DurationMinutes)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	minutes, err = ValidateLeaseDuration(minutes, settings.MaxMinutes)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	targets, err := validateTargets(body.TargetUUIDs)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	global, user, err := countActiveLeases(principal.UserUUID)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if global >= MaxActiveLeases || user >= MaxActiveLeasesPerUser {
		api.RespondError(c, http.StatusTooManyRequests, "too many active MCP authorizations")
		return
	}
	now := time.Now().UTC()
	leaseID, err := newID("ls_")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "failed to create authorization")
		return
	}
	familyID, err := newID("tf_")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "failed to create authorization")
		return
	}
	lease := models.MCPLease{
		ID:                     leaseID,
		OwnerUserUUID:          principal.UserUUID,
		OwnerLoginSessionHash:  accounts.SessionLookupKey(loginSession),
		OAuthClientID:          req.ClientID,
		AuthorizationRequestID: req.ID,
		TokenFamilyID:          familyID,
		TargetUUIDs:            encodeTargetUUIDs(targets),
		Mode:                   modeFull,
		Note:                   strings.TrimSpace(body.Note),
		MaxConcurrency:         settings.MaxConcurrency,
		Status:                 statusActive,
		PolicyVersion:          PolicyVersion,
		CreatedAt:              now,
		ExpiresAt:              leaseExpiresAt(now, minutes),
	}
	if err := database().Create(&lease).Error; err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	code, err := issueAuthorizationCode(lease, req, now)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	_ = database().Model(&req).Updates(map[string]any{
		"status":          statusActive,
		"owner_user_uuid": principal.UserUUID,
	}).Error
	redirect, _ := urlWithCode(req.RedirectURI, code, req.State)
	auditlog.Event(c.ClientIP(), principal.UserUUID, "warn", "audit.mcp_approve", map[string]string{"id": lease.ID})
	api.RespondSuccess(c, gin.H{
		"lease_id":     lease.ID,
		"expires_at":   lease.ExpiresAt.UTC(),
		"redirect_uri": redirect,
	})
}

func denyAuthorization(c *gin.Context) {
	principal, loginSession, ok := requireAdminSession(c)
	if !ok {
		return
	}
	req, err := loadAuthRequest(c.Param("id"))
	if err != nil || req.Status != statusPending || !req.ExpiresAt.After(time.Now().UTC()) {
		api.RespondError(c, http.StatusNotFound, "authorization request not found")
		return
	}
	now := time.Now().UTC()
	lease, err := recordDeniedAuthorization(database(), req, principal.UserUUID, accounts.SessionLookupKey(loginSession), now)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	redirect, _ := urlWithError(req.RedirectURI, "access_denied", req.State)
	auditlog.Event(c.ClientIP(), principal.UserUUID, "warn", "audit.mcp_deny", map[string]string{"id": lease.ID})
	api.RespondSuccess(c, gin.H{
		"lease_id":     lease.ID,
		"redirect_uri": redirect,
	})
}

func purgeHistory(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	deleted, err := purgeInactiveHistory(database(), time.Now().UTC())
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	api.RespondSuccess(c, gin.H{"deleted": deleted})
}

func listLeases(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	var leases []models.MCPLease
	now := time.Now().UTC()
	_ = database().Where("created_at >= ?", adminHistoryCutoff(now)).Order("created_at DESC").Find(&leases).Error
	items := make([]gin.H, 0, len(leases))
	for _, lease := range leases {
		if lease.Status == statusActive && !lease.ExpiresAt.After(now) {
			lease.Status = statusExpired
		}
		client, _ := loadClient(lease.OAuthClientID)
		ownerName := ""
		if owner, err := accounts.GetUserByUUID(lease.OwnerUserUUID); err == nil {
			ownerName = owner.Username
		}
		items = append(items, gin.H{
			"id":              lease.ID,
			"status":          lease.Status,
			"client_id":       lease.OAuthClientID,
			"client_name":     client.ClientName,
			"owner_username":  ownerName,
			"note":            lease.Note,
			"mode":            lease.Mode,
			"target_uuids":    parseTargetUUIDs(lease.TargetUUIDs),
			"created_at":      lease.CreatedAt.UTC(),
			"expires_at":      lease.ExpiresAt.UTC(),
			"revoked_at":      lease.RevokedAt,
			"max_concurrency": lease.MaxConcurrency,
			"running":         runningOperationCount(lease.ID),
		})
	}
	api.RespondSuccess(c, gin.H{"leases": items})
}

func getLease(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	var lease models.MCPLease
	if err := database().Where("id = ?", c.Param("id")).First(&lease).Error; err != nil {
		api.RespondError(c, http.StatusNotFound, "authorization not found")
		return
	}
	client, _ := loadClient(lease.OAuthClientID)
	api.RespondSuccess(c, gin.H{
		"id":              lease.ID,
		"status":          lease.Status,
		"client_name":     client.ClientName,
		"note":            lease.Note,
		"target_uuids":    parseTargetUUIDs(lease.TargetUUIDs),
		"created_at":      lease.CreatedAt.UTC(),
		"expires_at":      lease.ExpiresAt.UTC(),
		"max_concurrency": lease.MaxConcurrency,
		"running":         runningOperationCount(lease.ID),
	})
}

func revokeLease(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	var lease models.MCPLease
	if err := database().Where("id = ?", c.Param("id")).First(&lease).Error; err != nil {
		api.RespondError(c, http.StatusNotFound, "authorization not found")
		return
	}
	_ = revokeFamily(lease.TokenFamilyID, reasonRevoked)
	api.RespondSuccess(c, gin.H{"revoked": true})
}

func revokeAllLeases(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	DrainMCPDelivery()
	api.RespondSuccess(c, gin.H{"revoked": true})
}

func listOperations(c *gin.Context) {
	if _, _, ok := requireAdminSession(c); !ok {
		return
	}
	maybeCompactOperationOutputs()
	now := time.Now().UTC()
	query := database().Model(&models.MCPOperation{}).
		Select(fmt.Sprintf("id, lease_id, agent_uuid, tool_name, state, exit_code, truncated, substr(output, 1, %d) AS output, created_at, started_at, finished_at", AdminOutputPreviewMax)).
		Where("created_at >= ?", adminHistoryCutoff(now)).
		Order("created_at DESC")
	if leaseID := strings.TrimSpace(c.Query("lease_id")); leaseID != "" {
		query = query.Where("lease_id = ?", leaseID)
	}
	if agentUUID := strings.TrimSpace(c.Query("agent_uuid")); agentUUID != "" {
		query = query.Where("agent_uuid = ?", agentUUID)
	}
	var ops []models.MCPOperation
	_ = query.Find(&ops).Error
	items := make([]gin.H, 0, len(ops))
	for _, op := range ops {
		items = append(items, gin.H{
			"id":          op.ID,
			"lease_id":    op.LeaseID,
			"agent_uuid":  op.AgentUUID,
			"tool_name":   op.ToolName,
			"state":       op.State,
			"exit_code":   op.ExitCode,
			"truncated":   op.Truncated,
			"preview":     clipText(op.Output, AdminOutputPreviewMax),
			"created_at":  op.CreatedAt.UTC(),
			"started_at":  op.StartedAt,
			"finished_at": op.FinishedAt,
		})
	}
	api.RespondSuccess(c, gin.H{"operations": items})
}

func loadAuthRequest(id string) (models.MCPAuthorizationRequest, error) {
	var req models.MCPAuthorizationRequest
	err := database().Where("id = ?", id).First(&req).Error
	return req, err
}

func validateTargets(ids []string) ([]string, error) {
	ids = compactStrings(ids)
	if len(ids) == 0 {
		return nil, errTool("select at least one node")
	}
	known, err := clients.GetClientsByUUIDs(ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		node, ok := known[id]
		if !ok {
			return nil, errTool("node not found: " + id)
		}
		if err := remote.AgentRemoteAllowed(node); err != nil {
			return nil, err
		}
		if !agentSupportsMCP(node) {
			return nil, ErrAgentUnsupported
		}
	}
	return ids, nil
}

func urlWithCode(redirectURI, code, state string) (string, error) {
	parsed, err := parseRedirectURI(redirectURI)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("code", code)
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func urlWithError(redirectURI, code, state string) (string, error) {
	parsed, err := parseRedirectURI(redirectURI)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("error", code)
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

var ansiEscapePattern = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)

func stripANSI(value string) string {
	if value == "" {
		return value
	}
	return strings.ReplaceAll(ansiEscapePattern.ReplaceAllString(value, ""), "\r", "")
}

func clipText(value string, max int) string {
	value = strings.TrimSpace(stripANSI(value))
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	return string([]rune(value)[:max])
}
