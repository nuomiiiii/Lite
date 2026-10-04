package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/api/remote"
	"gorm.io/gorm"
)

var (
	refreshMu sync.Mutex
	refreshIn = map[string]*refreshWait{}

	registerMu   sync.Mutex
	registerHits = map[string][]time.Time{}
	authorizeMu  sync.Mutex
)

const (
	maxRegisterBodyBytes   = 8 << 10
	maxClientNameRunes     = 120
	maxRedirectURICount    = 8
	maxRedirectURILength   = 512
	maxRegisterPerIPMinute = 10
	maxUnusedMCPClients    = 256
	refreshWaitTimeout     = 20 * time.Second
)

type refreshWait struct {
	done chan struct{}
	pair tokenPair
}

type tokenPair struct {
	Access    string
	Refresh   string
	ExpiresIn int
	Scope     string
	TokenType string
	Resource  string
	LeaseID   string
	err       error
}

func RegisterPublic(r gin.IRouter) {
	startMCPBackground()
	registerPublicRoutes(r)
}

func registerPublicRoutes(r gin.IRouter) {
	g := r.Group("")
	g.Use(hidePublicMCP)
	g.GET("/.well-known/oauth-protected-resource", wellKnownProtectedResource)
	g.GET("/.well-known/oauth-protected-resource/mcp", wellKnownProtectedResource)
	g.GET("/.well-known/oauth-authorization-server", wellKnownAuthorizationServer)
	g.Any("/oauth", rejectPublicMCP)
	g.Any("/oauth/*path", dispatchOAuth)
	g.Any("/mcp", serveMCP)
	g.Any("/mcp/*path", rejectPublicMCP)
}

func dispatchOAuth(c *gin.Context) {
	switch strings.Trim(c.Param("path"), "/") {
	case "register":
		serveRegister(c)
	case "authorize":
		serveAuthorize(c)
	case "token":
		serveToken(c)
	case "revoke":
		serveRevoke(c)
	default:
		rejectPublicMCP(c)
	}
}

func serveMCP(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead:
		handleMCPGet(c)
	case http.MethodPost:
		handleMCP(c)
	case http.MethodOptions:
		handleMCORS(c)
	default:
		rejectPublicMCP(c)
	}
}

func rejectPublicMCP(c *gin.Context) {
	c.AbortWithStatus(http.StatusNotFound)
}

func hidePublicMCP(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	c.Next()
}

func abortIfMCPDisabled(c *gin.Context) bool {
	if isMCPEnabled() {
		return false
	}
	c.AbortWithStatus(http.StatusNotFound)
	return true
}

func requestIsHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")))
	return proto == "https"
}

func instanceBase(c *gin.Context) string {
	return publicBaseURL(requestScheme(requestIsHTTPS(c)), c.Request.Host)
}

func instanceResource(c *gin.Context) string {
	return resourceURL(instanceBase(c))
}

func wellKnownProtectedResource(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	applyMCORS(c)
	base := instanceBase(c)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"resource":                 resourceURL(base),
		"authorization_servers":    []string{base},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{scopeAgentFull},
		"resource_name":            "Lite MCP",
	})
}

func wellKnownAuthorizationServer(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	applyMCORS(c)
	base := instanceBase(c)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{pkceS256},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{scopeAgentFull},
		"resource_indicators_supported":         true,
	})
}

func serveRegister(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	switch c.Request.Method {
	case http.MethodPost:
		handleRegister(c)
	case http.MethodOptions:
		handleMCORS(c)
	default:
		c.AbortWithStatus(http.StatusNotFound)
	}
}

func serveAuthorize(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	switch c.Request.Method {
	case http.MethodGet:
		handleAuthorize(c)
	case http.MethodOptions:
		handleMCORS(c)
	default:
		c.AbortWithStatus(http.StatusNotFound)
	}
}

func serveToken(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	switch c.Request.Method {
	case http.MethodPost:
		handleToken(c)
	case http.MethodOptions:
		handleMCORS(c)
	default:
		c.AbortWithStatus(http.StatusNotFound)
	}
}

func serveRevoke(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	switch c.Request.Method {
	case http.MethodPost:
		handleRevoke(c)
	case http.MethodOptions:
		handleMCORS(c)
	default:
		c.AbortWithStatus(http.StatusNotFound)
	}
}

func handleRegister(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	applyMCORS(c)
	c.Header("Cache-Control", "no-store")
	if !allowMCPRegister(c.ClientIP(), time.Now().UTC()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "temporarily_unavailable"})
		return
	}
	_ = pruneUnusedMCPClients(database(), time.Now().UTC())
	if unusedMCPClientCount() >= maxUnusedMCPClients {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "temporarily_unavailable"})
		return
	}
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRegisterBodyBytes)
	}
	var body struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.RedirectURIs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_client_metadata"})
		return
	}
	if len(body.RedirectURIs) > maxRedirectURICount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_client_metadata"})
		return
	}
	name := strings.TrimSpace(body.ClientName)
	if utf8.RuneCountInString(name) > maxClientNameRunes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_client_metadata"})
		return
	}
	allowed := make([]string, 0, len(body.RedirectURIs))
	for _, item := range body.RedirectURIs {
		if len(item) > maxRedirectURILength {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_redirect_uri"})
			return
		}
		if _, err := parseRedirectURI(item); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_redirect_uri"})
			return
		}
		allowed = append(allowed, item)
	}
	id, err := newID("cli_")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	client := models.MCPClient{
		ClientID:                id,
		ClientName:              name,
		RedirectURIs:            encodeJSONList(allowed),
		TokenEndpointAuthMethod: "none",
		CreatedAt:               time.Now().UTC(),
	}
	if err := saveClient(client); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	_ = queueRegisteredClientAuthorization(client, allowed[0], client.CreatedAt)
	c.JSON(http.StatusCreated, gin.H{
		"client_id":                  client.ClientID,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"client_name":                client.ClientName,
		"redirect_uris":              allowed,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

func allowMCPRegister(ip string, now time.Time) bool {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	window := now.Add(-time.Minute)
	registerMu.Lock()
	defer registerMu.Unlock()
	next := registerHits[ip][:0]
	for _, hit := range registerHits[ip] {
		if hit.After(window) {
			next = append(next, hit)
		}
	}
	if len(next) >= maxRegisterPerIPMinute {
		registerHits[ip] = next
		return false
	}
	registerHits[ip] = append(next, now)
	return true
}

func handleAuthorize(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	query := c.Request.URL.Query()
	if query.Get("response_type") != "code" {
		oauthAuthorizeError(c, query.Get("redirect_uri"), query.Get("state"), "unsupported_response_type")
		return
	}
	clientID := strings.TrimSpace(query.Get("client_id"))
	redirectURI := strings.TrimSpace(query.Get("redirect_uri"))
	challenge := strings.TrimSpace(query.Get("code_challenge"))
	method := strings.TrimSpace(query.Get("code_challenge_method"))
	if method == "" {
		method = pkceS256
	}
	if !strings.EqualFold(method, pkceS256) || challenge == "" {
		oauthAuthorizeError(c, redirectURI, query.Get("state"), "invalid_request")
		return
	}
	client, err := loadClient(clientID)
	if err != nil {
		c.String(http.StatusBadRequest, "unknown client")
		return
	}
	if !exactRedirectMatch(splitJSONList(client.RedirectURIs), redirectURI) {
		c.String(http.StatusBadRequest, "invalid redirect_uri")
		return
	}
	resource := strings.TrimSpace(query.Get("resource"))
	if resource == "" {
		resource = instanceResource(c)
	}
	if !sameResource(resource, instanceResource(c)) {
		oauthAuthorizeError(c, redirectURI, query.Get("state"), "invalid_target")
		return
	}
	now := time.Now().UTC()
	req, err := createAuthorizationRequest(models.MCPAuthorizationRequest{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		State:               query.Get("state"),
		CodeChallenge:       challenge,
		CodeChallengeMethod: pkceS256,
		Resource:            resource,
		Scope:               scopeAgentFull,
		Status:              statusPending,
		CreatedAt:           now,
		ExpiresAt:           now.Add(10 * time.Minute),
	})
	if err != nil {
		if errors.Is(err, errAuthorizationDenied) {
			oauthAuthorizeError(c, redirectURI, query.Get("state"), "access_denied")
			return
		}
		oauthAuthorizeError(c, redirectURI, query.Get("state"), "server_error")
		return
	}
	c.Redirect(http.StatusFound, "/admin/remote-management/mcp?authorize="+url.QueryEscape(req.ID))
}

func queueRegisteredClientAuthorization(client models.MCPClient, redirectURI string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := createAuthorizationRequest(models.MCPAuthorizationRequest{
		ClientID:            client.ClientID,
		RedirectURI:         redirectURI,
		CodeChallengeMethod: pkceS256,
		Scope:               scopeAgentFull,
		Status:              statusPending,
		CreatedAt:           now,
		ExpiresAt:           now.Add(10 * time.Minute),
	})
	return err
}

func createAuthorizationRequest(req models.MCPAuthorizationRequest) (models.MCPAuthorizationRequest, error) {
	authorizeMu.Lock()
	defer authorizeMu.Unlock()
	denied, err := clientHasDeniedAuthorization(req.ClientID)
	if err != nil {
		return models.MCPAuthorizationRequest{}, err
	}
	if denied {
		return models.MCPAuthorizationRequest{}, errAuthorizationDenied
	}
	now := req.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
		req.CreatedAt = now
	}
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = now.Add(10 * time.Minute)
	}
	var pending []models.MCPAuthorizationRequest
	if err := database().Where("status = ? AND client_id = ?", statusPending, req.ClientID).
		Order("created_at ASC").Find(&pending).Error; err != nil {
		return models.MCPAuthorizationRequest{}, err
	}
	for _, existing := range pending {
		if !existing.ExpiresAt.After(now) {
			continue
		}
		if sameAuthorizationIdentity(existing, req) {
			return existing, nil
		}
	}
	if strings.TrimSpace(req.CodeChallenge) != "" {
		for i := range pending {
			existing := pending[i]
			if !existing.ExpiresAt.After(now) || strings.TrimSpace(existing.CodeChallenge) != "" {
				continue
			}
			updates := map[string]any{
				"redirect_uri":          req.RedirectURI,
				"state":                 req.State,
				"code_challenge":        req.CodeChallenge,
				"code_challenge_method": req.CodeChallengeMethod,
				"resource":              req.Resource,
				"scope":                 req.Scope,
				"expires_at":            req.ExpiresAt,
			}
			if err := database().Model(&existing).Updates(updates).Error; err != nil {
				return models.MCPAuthorizationRequest{}, err
			}
			existing.RedirectURI = req.RedirectURI
			existing.State = req.State
			existing.CodeChallenge = req.CodeChallenge
			existing.CodeChallengeMethod = req.CodeChallengeMethod
			existing.Resource = req.Resource
			existing.Scope = req.Scope
			existing.ExpiresAt = req.ExpiresAt
			return existing, nil
		}
	}
	if req.ID == "" {
		id, err := newID("ar_")
		if err != nil {
			return models.MCPAuthorizationRequest{}, err
		}
		req.ID = id
	}
	if req.Status == "" {
		req.Status = statusPending
	}
	if err := database().Create(&req).Error; err != nil {
		return models.MCPAuthorizationRequest{}, err
	}
	return req, nil
}

func sameAuthorizationIdentity(a, b models.MCPAuthorizationRequest) bool {
	return a.RedirectURI == b.RedirectURI &&
		a.State == b.State &&
		a.CodeChallenge == b.CodeChallenge &&
		a.CodeChallengeMethod == b.CodeChallengeMethod &&
		a.Resource == b.Resource &&
		a.Scope == b.Scope
}

func authorizationRequestReady(req models.MCPAuthorizationRequest) bool {
	return strings.TrimSpace(req.CodeChallenge) != ""
}

var errAuthorizationDenied = errors.New("authorization denied")

func clientHasDeniedAuthorization(clientID string) (bool, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false, nil
	}
	var count int64
	if err := database().Model(&models.MCPAuthorizationRequest{}).
		Where("client_id = ? AND status = ?", clientID, statusDenied).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func oauthAuthorizeError(c *gin.Context, redirectURI, state, code string) {
	if _, err := parseRedirectURI(redirectURI); err != nil {
		c.String(http.StatusBadRequest, code)
		return
	}
	target, _ := url.Parse(redirectURI)
	query := target.Query()
	query.Set("error", code)
	if state != "" {
		query.Set("state", state)
	}
	target.RawQuery = query.Encode()
	c.Redirect(http.StatusFound, target.String())
}

func handleToken(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	applyMCORS(c)
	c.Header("Cache-Control", "no-store")
	values := tokenForm(c)
	switch values.Get("grant_type") {
	case "authorization_code":
		handleAuthorizationCode(c, values)
	case "refresh_token":
		handleRefreshToken(c, values)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_grant_type"})
	}
}

func tokenForm(c *gin.Context) url.Values {
	_ = c.Request.ParseForm()
	if len(c.Request.PostForm) > 0 {
		return c.Request.PostForm
	}
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err == nil {
		values := url.Values{}
		for key, raw := range body {
			switch typed := raw.(type) {
			case string:
				values.Set(key, typed)
			}
		}
		return values
	}
	return url.Values{}
}

func handleAuthorizationCode(c *gin.Context, values url.Values) {
	code := strings.TrimSpace(values.Get("code"))
	verifier := strings.TrimSpace(values.Get("code_verifier"))
	clientID := strings.TrimSpace(values.Get("client_id"))
	redirectURI := strings.TrimSpace(values.Get("redirect_uri"))
	resource := strings.TrimSpace(values.Get("resource"))
	now := time.Now().UTC()
	var token models.MCPToken
	if err := database().Where("hash = ? AND kind = ?", hashToken(code), kindCode).First(&token).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if token.Used || !token.ExpiresAt.After(now) || token.ClientID != clientID || token.RedirectURI != redirectURI {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if err := verifyPKCE(token.CodeChallengeMethod, token.CodeChallenge, verifier); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if resource != "" && !sameResource(resource, token.Resource) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_target"})
		return
	}
	lease, err := loadLiveLease(token.LeaseID, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	_ = database().Model(&token).Update("used", true).Error
	pair, err := issueTokenPair(lease, token.ClientID, token.RedirectURI, token.Resource, now)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	writeTokenResponse(c, pair)
}

func handleRefreshToken(c *gin.Context, values url.Values) {
	refresh := strings.TrimSpace(values.Get("refresh_token"))
	clientID := strings.TrimSpace(values.Get("client_id"))
	now := time.Now().UTC()
	var token models.MCPToken
	if err := database().Where("hash = ? AND kind = ?", hashToken(refresh), kindRefresh).First(&token).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	// Validate client binding BEFORE joining a concurrent/retry response.
	if clientID != "" && token.ClientID != clientID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_client"})
		return
	}
	lease, err := loadLiveLease(token.LeaseID, now)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	refreshMu.Lock()
	if pending, ok := refreshIn[refresh]; ok {
		refreshMu.Unlock()
		select {
		case <-pending.done:
		case <-c.Request.Context().Done():
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "temporarily_unavailable"})
			return
		case <-time.After(refreshWaitTimeout):
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "temporarily_unavailable"})
			return
		}
		if pending.pair.err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
			return
		}
		if _, err := loadLiveLease(token.LeaseID, time.Now().UTC()); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
			return
		}
		writeTokenResponse(c, pending.pair)
		return
	}
	wait := &refreshWait{done: make(chan struct{})}
	refreshIn[refresh] = wait
	refreshMu.Unlock()
	defer func() {
		close(wait.done)
		if wait.pair.err != nil {
			refreshMu.Lock()
			delete(refreshIn, refresh)
			refreshMu.Unlock()
			return
		}
		// Briefly tolerate a lost HTTP response; later reuse still revokes.
		// Plaintext successor credentials are never persisted to disk.
		time.AfterFunc(30*time.Second, func() {
			refreshMu.Lock()
			if refreshIn[refresh] == wait {
				delete(refreshIn, refresh)
			}
			refreshMu.Unlock()
		})
	}()
	if token.Used {
		_ = revokeFamily(token.FamilyID, reasonRefreshReuse)
		wait.pair = tokenPair{err: ErrTokenReuse}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	if !token.ExpiresAt.After(now) {
		wait.pair = tokenPair{err: ErrLeaseInactive}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}
	var pair tokenPair
	err = database().Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.MCPToken{}).Where("hash = ? AND used = ? AND expires_at > ?", token.Hash, false, now).Update("used", true)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrTokenReuse
		}
		var issueErr error
		pair, issueErr = issueTokenPairInDB(tx, lease, token.ClientID, token.RedirectURI, token.Resource, now)
		return issueErr
	})
	if err != nil {
		wait.pair = tokenPair{err: err}
		if errors.Is(err, ErrTokenReuse) {
			_ = revokeFamily(token.FamilyID, reasonRefreshReuse)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_error"})
		return
	}
	wait.pair = pair
	writeTokenResponse(c, pair)
}

func issueTokenPair(lease models.MCPLease, clientID, redirectURI, resource string, now time.Time) (tokenPair, error) {
	return issueTokenPairInDB(database(), lease, clientID, redirectURI, resource, now)
}

func issueTokenPairInDB(db *gorm.DB, lease models.MCPLease, clientID, redirectURI, resource string, now time.Time) (tokenPair, error) {
	accessTTL := truncateTTL(now, lease.ExpiresAt, AccessTokenTTL)
	refreshTTL := truncateTTL(now, lease.ExpiresAt, lease.ExpiresAt.Sub(now))
	if accessTTL <= 0 || refreshTTL <= 0 {
		return tokenPair{}, ErrLeaseInactive
	}
	accessPlain, accessHash, err := newOpaqueToken(tokenPrefixAccess)
	if err != nil {
		return tokenPair{}, err
	}
	refreshPlain, refreshHash, err := newOpaqueToken(tokenPrefixRefresh)
	if err != nil {
		return tokenPair{}, err
	}
	access := models.MCPToken{
		Hash:        accessHash,
		Kind:        kindAccess,
		FamilyID:    lease.TokenFamilyID,
		LeaseID:     lease.ID,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		Resource:    resource,
		ExpiresAt:   now.Add(accessTTL),
		CreatedAt:   now,
	}
	refresh := models.MCPToken{
		Hash:        refreshHash,
		Kind:        kindRefresh,
		FamilyID:    lease.TokenFamilyID,
		LeaseID:     lease.ID,
		ClientID:    clientID,
		RedirectURI: redirectURI,
		Resource:    resource,
		ExpiresAt:   now.Add(refreshTTL),
		CreatedAt:   now,
	}
	if err := db.Create(&access).Error; err != nil {
		return tokenPair{}, err
	}
	if err := db.Create(&refresh).Error; err != nil {
		return tokenPair{}, err
	}
	return tokenPair{
		Access:    accessPlain,
		Refresh:   refreshPlain,
		ExpiresIn: int(accessTTL / time.Second),
		Scope:     scopeAgentFull,
		TokenType: "Bearer",
		Resource:  resource,
		LeaseID:   lease.ID,
	}, nil
}

func writeTokenResponse(c *gin.Context, pair tokenPair) {
	c.JSON(http.StatusOK, gin.H{
		"access_token":  pair.Access,
		"refresh_token": pair.Refresh,
		"token_type":    pair.TokenType,
		"expires_in":    pair.ExpiresIn,
		"scope":         pair.Scope,
		"resource":      pair.Resource,
	})
}

func handleRevoke(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	applyMCORS(c)
	c.Header("Cache-Control", "no-store")
	values := tokenForm(c)
	token := strings.TrimSpace(values.Get("token"))
	if token == "" {
		c.Status(http.StatusOK)
		return
	}
	var stored models.MCPToken
	if err := database().Where("hash = ?", hashToken(token)).First(&stored).Error; err != nil {
		c.Status(http.StatusOK)
		return
	}
	_ = revokeFamily(stored.FamilyID, reasonRevoked)
	c.Status(http.StatusOK)
}

func revokeFamily(familyID, reason string) error {
	now := time.Now().UTC()
	var lease models.MCPLease
	if err := database().Where("token_family_id = ?", familyID).First(&lease).Error; err == nil {
		_ = database().Model(&lease).Updates(map[string]any{
			"status":            statusRevoked,
			"revoked_at":        now,
			"revocation_reason": reason,
		}).Error
		cancelLeaseOperations(lease.ID)
		remote.CloseMCPLeaseSessions(lease.ID)
		_ = compactLeaseOperationOutputs(database(), lease.ID)
	}
	return database().Model(&models.MCPToken{}).Where("family_id = ?", familyID).Updates(map[string]any{
		"used":       true,
		"expires_at": now,
	}).Error
}

func lookupAccess(plain string, now time.Time) (models.MCPToken, models.MCPLease, error) {
	var token models.MCPToken
	if err := database().Where("hash = ? AND kind = ?", hashToken(plain), kindAccess).First(&token).Error; err != nil {
		return models.MCPToken{}, models.MCPLease{}, err
	}
	if token.Used || !token.ExpiresAt.After(now) {
		return models.MCPToken{}, models.MCPLease{}, ErrLeaseInactive
	}
	lease, err := loadLiveLease(token.LeaseID, now)
	return token, lease, err
}

func issueAuthorizationCode(lease models.MCPLease, req models.MCPAuthorizationRequest, now time.Time) (string, error) {
	plain, hash, err := newOpaqueToken(tokenPrefixCode)
	if err != nil {
		return "", err
	}
	token := models.MCPToken{
		Hash:                hash,
		Kind:                kindCode,
		FamilyID:            lease.TokenFamilyID,
		LeaseID:             lease.ID,
		ClientID:            req.ClientID,
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		Resource:            req.Resource,
		ExpiresAt:           now.Add(AuthCodeTTL),
		CreatedAt:           now,
	}
	if err := database().Create(&token).Error; err != nil {
		return "", err
	}
	return plain, nil
}

func applyMCORS(c *gin.Context) {
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	if origin == "" {
		return
	}
	c.Header("Access-Control-Allow-Origin", origin)
	c.Header("Vary", "Origin")
	c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version, Mcp-Session-Id, Last-Event-ID, Accept")
	c.Header("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate")
	c.Header("Access-Control-Max-Age", "600")
}

func handleMCORS(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	applyMCORS(c)
	c.Status(http.StatusNoContent)
}

func mcpChallenge(c *gin.Context, errCode, message string) {
	applyMCORS(c)
	c.Header("Cache-Control", "no-store")
	c.Header("WWW-Authenticate", `Bearer realm="Lite MCP", resource_metadata="`+instanceBase(c)+`/.well-known/oauth-protected-resource/mcp"`)
	status := http.StatusUnauthorized
	if errCode == "insufficient_scope" {
		status = http.StatusForbidden
	}
	c.JSON(status, gin.H{"error": errCode, "error_description": message})
}

func jsonRPCError(id any, code int, message string) gin.H {
	return gin.H{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   gin.H{"code": code, "message": message},
	}
}

func jsonRPCResult(id any, result any) gin.H {
	return gin.H{"jsonrpc": "2.0", "id": id, "result": result}
}

func parseJSONRPC(body []byte) ([]map[string]any, bool, error) {
	body = bytesTrim(body)
	if len(body) == 0 {
		return nil, false, errors.New("empty body")
	}
	if body[0] == '[' {
		var batch []map[string]any
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, true, err
		}
		return batch, true, nil
	}
	var single map[string]any
	if err := json.Unmarshal(body, &single); err != nil {
		return nil, false, err
	}
	return []map[string]any{single}, false, nil
}

func bytesTrim(body []byte) []byte {
	return []byte(strings.TrimSpace(string(body)))
}

func bearerToken(c *gin.Context) string {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[len("Bearer "):])
}

func siteAPIKey(c *gin.Context) bool {
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	return api.IsSiteAPIKey(header)
}

func handleMCPGet(c *gin.Context) {
	if abortIfMCPDisabled(c) {
		return
	}
	plain := bearerToken(c)
	if plain == "" {
		mcpChallenge(c, "invalid_token", "MCP access token is required")
		return
	}
	if siteAPIKey(c) {
		mcpChallenge(c, "invalid_token", ErrAPIKeyForbidden.Error())
		return
	}
	now := time.Now().UTC()
	if _, _, err := lookupAccess(plain, now); err != nil {
		mcpChallenge(c, "invalid_token", "MCP access token is invalid or expired")
		return
	}
	if !remote.RemoteManagementEnabled() {
		mcpChallenge(c, "invalid_token", ErrRemoteDisabled.Error())
		return
	}
	applyMCORS(c)
	c.Header("Allow", "POST, OPTIONS")
	c.Status(http.StatusMethodNotAllowed)
}
