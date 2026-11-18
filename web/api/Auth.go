package api

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"gorm.io/gorm"
)

const cachedSessionStateKey = "cached_session_auth_state"

type CachedSessionAuth struct {
	SessionToken string
	UserUUID     string
	User         *models.User
}

func SetCachedSessionAuth(c *gin.Context, auth CachedSessionAuth) {
	if c == nil {
		return
	}
	c.Set(cachedSessionStateKey, auth)
}

func GetCachedSessionAuth(c *gin.Context, session string) (CachedSessionAuth, bool) {
	if c == nil || session == "" {
		return CachedSessionAuth{}, false
	}
	raw, ok := c.Get(cachedSessionStateKey)
	if !ok {
		return CachedSessionAuth{}, false
	}
	state, ok := raw.(CachedSessionAuth)
	if !ok || state.SessionToken != session {
		return CachedSessionAuth{}, false
	}
	return state, true
}

// ResolvePrincipalAndCacheSession identifies the principal and caches the validated
// session + user on gin.Context so downstream buildContextMeta does not re-query User.
func ResolvePrincipalAndCacheSession(c *gin.Context) *rpc.Principal {
	if isApiKeyValid(c.GetHeader("Authorization")) {
		return rpc.NewAPIKeyPrincipal()
	}
	if session, err := c.Cookie("session_token"); err == nil && session != "" {
		if uuid, err := accounts.GetSession(session); err == nil && uuid != "" {
			if user, uerr := accounts.GetUserByUUID(uuid); uerr == nil {
				SetCachedSessionAuth(c, CachedSessionAuth{
					SessionToken: session,
					UserUUID:     uuid,
					User:         &user,
				})
				return rpc.NewUserPrincipal(uuid)
			}
			return rpc.NewAnonymousPrincipal()
		}
	}
	if token := extractClientToken(c); token != "" {
		if uuid, err := checkTokenAndGetUUID(token); err == nil && uuid != "" {
			return rpc.NewAgentPrincipal(uuid)
		}
	}
	return rpc.NewAnonymousPrincipal()
}

const (
	RoleAdmin  = "admin"
	RoleClient = "client"
	RoleGuest  = "guest"
)

// IdentityMiddleware 统一身份识别中间件，在路由栈最外层运行。
// 负责识别当前请求者身份（Admin / Client / Guest），并写入 Context。
// 身份识别统一委托给 IdentifyPrincipal;同时保留旧的 c.Set 键(role/uuid/
// api_key/session/client_uuid)以兼容现有 handler 与中间件。
func IdentityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := ResolvePrincipalAndCacheSession(c)
		SetPrincipal(c, p)

		// 写入兼容字段。
		c.Set("role", p.PrimaryRole())
		switch p.Type {
		case rpc.PrincipalAPIKey:
			// 旧逻辑:API Key 时记录裸 key 与固定占位 uuid。
			apiKey := c.GetHeader("Authorization")
			c.Set("api_key", apiKey[len("Bearer "):])
			c.Set("uuid", "00000000-0000-0000-0000-000000000000")
		case rpc.PrincipalUser:
			if session, err := c.Cookie("session_token"); err == nil && session != "" {
				c.Set("session", session)
			}
			c.Set("uuid", p.UserUUID)
		case rpc.PrincipalAgent:
			c.Set("client_uuid", p.ClientUUID)
		}

		c.Next()
	}
}

func RejectAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := GetPrincipal(c)
		if p != nil && (p.IsAPIKey || p.Type == rpc.PrincipalAPIKey) {
			RespondError(c, http.StatusForbidden, "API keys cannot perform this action")
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireRole 声明式权限校验中间件，仅允许指定角色通过。
func RequireRole(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		current := GetRole(c)
		for _, role := range allowedRoles {
			if current == role {
				c.Next()
				return
			}
		}
		RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		c.Abort()
	}
}

// GetRole 获取当前请求的角色
func GetRole(c *gin.Context) string {
	role, exists := c.Get("role")
	if !exists {
		return RoleGuest
	}
	if s, ok := role.(string); ok {
		return s
	}
	return RoleGuest
}

// --- 私有站点访问控制 ---

var publicPaths = []string{
	"/ping",
	"/api/public",
	"/api/login",
	"/api/session/touch",
	"/api/passkeys/",
	"/api/me",
	"/api/oauth",
	"/api/oauth_callback",
	"/api/version",
	"/api/recent",
	"/api/admin",    // 由 RequireRole 处理
	"/api/clients/", // 由 RequireRole 处理
}

// PrivateSiteMiddleware 私有站点访问控制。
// 依赖 IdentityMiddleware 已设置的 role，对未认证的访客在私有站点模式下进行拦截。
func PrivateSiteMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 已认证用户直接放行
		if GetRole(c) != RoleGuest {
			c.Next()
			return
		}

		path := c.Request.URL.Path

		// 公开路径直接放行
		for _, p := range publicPaths {
			if strings.HasPrefix(path, p) {
				c.Next()
				return
			}
		}

		// 非 API 路径直接放行（静态资源等）
		if !strings.HasPrefix(path, "/api") {
			c.Next()
			return
		}

		// 非私有站点直接放行
		privateSite, err := config.GetAs[bool](config.PrivateSiteKey, false)
		if err != nil {
			RespondError(c, http.StatusInternalServerError, "Failed to get configuration.")
			c.Abort()
			return
		}
		if !privateSite {
			c.Next()
			return
		}

		// 临时访问许可
		if hasTempAccess(c) {
			c.Next()
			return
		}

		RespondError(c, http.StatusUnauthorized, "Private site is enabled, please login first.")
		c.Abort()
	}
}

func hasTempAccess(c *gin.Context) bool {
	tempKey, err := c.Cookie("temp_key")
	if err != nil || tempKey == "" {
		return false
	}
	expireAt, err := config.GetAs[int64]("tempory_share_token_expire_at", 0)
	if err != nil {
		return false
	}
	allowKey, err := config.GetAs[string]("tempory_share_token", "")
	if err != nil || allowKey == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(tempKey), []byte(allowKey)) != 1 {
		return false
	}
	return expireAt >= time.Now().Unix()
}

func extractClientToken(c *gin.Context) string {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(authorization, "Bearer ") && !isApiKeyValid(authorization) {
		if token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")); token != "" {
			return token
		}
	}
	return ""
}

func checkTokenAndGetUUID(token string) (string, error) {
	uuid, err := clients.GetClientUUIDByToken(token)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err == gorm.ErrRecordNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return uuid, nil
}

func isApiKeyValid(apiKey string) bool {
	if !strings.HasPrefix(apiKey, "Bearer ") {
		return false
	}
	apiKeyConfig, err := config.GetAs[string](config.ApiKeyKey, "")
	if err != nil {
		return false
	}

	if apiKeyConfig == "" || len(apiKeyConfig) < 12 {
		return false
	}
	token := apiKey[len("Bearer "):]
	return subtle.ConstantTimeCompare([]byte(token), []byte(apiKeyConfig)) == 1
}

// IsSiteAPIKey reports whether Authorization is the site-wide API key.
func IsSiteAPIKey(authorization string) bool {
	return isApiKeyValid(strings.TrimSpace(authorization))
}
