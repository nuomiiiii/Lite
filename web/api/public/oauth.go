package public

import (
	"fmt"
	"net/url"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils"
	"github.com/nuomiiiii/lite/web/oauth"
	"github.com/nuomiiiii/lite/web/passkey"
)

// /api/oauth
func OAuth(c *gin.Context) {
	OAuthEnabled, _ := config.GetAs[bool](config.OAuthEnabledKey, false)
	if !OAuthEnabled {
		c.JSON(403, gin.H{"status": "error", "error": "OAuth is not enabled"})
		return
	}

	authURL, state := oauth.CurrentProvider().GetAuthorizationURL(utils.GetCallbackURL(c))

	c.SetCookie("oauth_state", state, 3600, "/", "", false, true)

	c.Redirect(302, authURL)
}

// /api/oauth_callback
func OAuthCallback(c *gin.Context) {

	// 验证state防止CSRF攻击
	state, _ := c.Cookie("oauth_state")
	c.SetCookie("oauth_state", "", -1, "/", "", false, true)

	// 获取当前OAuth提供商名称
	providerName := oauth.CurrentProvider().GetName()

	providersSkipStateCheck := []string{"qq"}
	if slices.Contains(providersSkipStateCheck, providerName) {
		// 对于QQ登录，由于是通过QQ聚合登录平台中转，state可能会不匹配
		// 但我们仍然需要验证state的存在性（不能是空的）
		if state == "" {
			c.JSON(400, gin.H{"status": "error", "error": "Invalid state"})
			return
		}
	} else {
		// 对于其他提供商，严格验证state匹配
		if state == "" || state != c.Query("state") {
			c.JSON(400, gin.H{"status": "error", "error": "Invalid state"})
			return
		}
	}

	queries := make(map[string]string)
	for key, values := range c.Request.URL.Query() {
		if len(values) > 0 {
			queries[key] = values[0]
		}
	}
	oidcUser, err := oauth.CurrentProvider().OnCallback(c, state, queries, utils.GetCallbackURL(c))
	if err != nil {
		if _, bindErr := c.Cookie("binding_external_account"); bindErr == nil {
			redirectOAuthAccountError(c, "oauth_error")
			return
		}
		if _, confirmErr := c.Cookie("passkey_confirm_account"); confirmErr == nil {
			redirectPasskeyConfirmError(c, "passkey_confirm_failed")
			return
		}
		c.JSON(500, gin.H{"status": "error", "error": "Failed to get user info: " + err.Error()})
		return
	}

	// ID作为SSO ID
	sso_id := fmt.Sprintf("%s_%s", oauth.CurrentProvider().GetName(), oidcUser.UserId)

	// 如果cookie中有binding_external_account，说明是绑定外部账号
	// 否则如果是 passkey_confirm_account，说明是添加通行密钥时的 SSO 确认
	// 否则是登录
	uuid, _ := c.Cookie("binding_external_account")
	c.SetCookie("binding_external_account", "", -1, "/", "", false, true)
	if uuid != "" {
		// 绑定外部账号
		session, _ := c.Cookie("session_token")
		user, err := accounts.GetUserBySession(session)
		if err != nil || user.UUID != uuid {
			redirectOAuthAccountError(c, "bind_failed")
			return
		}
		err = accounts.BindingExternalAccount(user.UUID, sso_id)
		if err != nil {
			redirectOAuthAccountError(c, "bind_failed")
			return
		}
		auditlog.Event(c.ClientIP(), user.UUID, "login", "audit.sso_bind", nil)
		c.Redirect(302, "/admin/settings/account-security?tab=github")
		return
	}

	confirmUUID, _ := c.Cookie("passkey_confirm_account")
	c.SetCookie("passkey_confirm_account", "", -1, "/", "", false, true)
	if confirmUUID != "" {
		session, _ := c.Cookie("session_token")
		user, err := accounts.GetUserBySession(session)
		sessionUUID := ""
		boundSSID := ""
		if err == nil {
			sessionUUID = user.UUID
			boundSSID = user.SSOID
		}
		if code := passkeySSOConfirmError(sessionUUID, confirmUUID, boundSSID, sso_id); code != "" {
			redirectPasskeyConfirmError(c, code)
			return
		}
		token := passkey.PutConfirmGrant(user.UUID)
		c.SetCookie("passkey_confirm_ok", token, int(passkeyConfirmCookieSeconds), "/", "", false, true)
		auditlog.Event(c.ClientIP(), user.UUID, "info", "audit.sso_passkey", nil)
		c.Redirect(302, "/admin/settings/account-security?tab=passkeys&passkey_confirm=ok")
		return
	}

	// 尝试获取用户
	user, err := accounts.GetUserBySSO(sso_id)
	if err != nil {
		c.JSON(401, gin.H{
			"status":  "error",
			"message": "please log in and bind your external account first.",
		})
		return
	}

	// 创建会话
	session, err := accounts.CreateSession(user.UUID, sessionCookieMaxAgeSeconds(), c.Request.UserAgent(), c.ClientIP(), "oauth")
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": err.Error()})
		return
	}

	// 设置cookie并返回
	setSessionCookie(c, session, sessionCookieMaxAgeSeconds())
	auditlog.Event(c.ClientIP(), user.UUID, "login", "audit.login_oauth", nil)
	c.Redirect(302, "/admin")
}

func redirectOAuthAccountError(c *gin.Context, code string) {
	switch code {
	case "bind_failed", "oauth_denied", "oauth_error":
	default:
		code = "oauth_error"
	}
	c.Redirect(302, "/admin/settings/account-security?tab=github&oauth_error="+url.QueryEscape(code))
}

const passkeyConfirmCookieSeconds = 300

func passkeySSOConfirmError(sessionUUID, cookieUUID, boundSSID, callbackSSID string) string {
	if sessionUUID == "" || cookieUUID == "" || sessionUUID != cookieUUID {
		return "passkey_confirm_failed"
	}
	if boundSSID == "" || boundSSID != callbackSSID {
		return "passkey_confirm_mismatch"
	}
	return ""
}

func redirectPasskeyConfirmError(c *gin.Context, code string) {
	switch code {
	case "passkey_confirm_failed", "passkey_confirm_mismatch":
	default:
		code = "passkey_confirm_failed"
	}
	c.SetCookie("passkey_confirm_account", "", -1, "/", "", false, true)
	c.SetCookie("passkey_confirm_ok", "", -1, "/", "", false, true)
	c.Redirect(302, "/admin/settings/account-security?tab=passkeys&oauth_error="+url.QueryEscape(code))
}
