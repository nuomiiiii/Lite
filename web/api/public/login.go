package public

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/remotectl"

	"github.com/gin-gonic/gin"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TwoFa    string `json:"2fa_code"`
}

const sessionCookieMaxAge = 2592000

func setSessionCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "session_token",
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   utils.GetScheme(c) == "https",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func Login(c *gin.Context) {
	DisablePasswordLogin, _ := config.GetAs[bool](config.DisablePasswordLoginKey, false)
	if DisablePasswordLogin {
		api.RespondError(c, http.StatusForbidden, "Password login is disabled")
		return
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	var data LoginRequest
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if data.Username == "" || data.Password == "" {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body: Username and password are required")
		return
	}

	uuid, err := accounts.AuthenticatePassword(data.Username, data.Password, c.ClientIP())
	if accounts.IsPasswordBusy(err) {
		api.RespondError(c, http.StatusTooManyRequests, err.Error())
		return
	}
	if err != nil || uuid == "" {
		api.RespondError(c, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	user, err := accounts.GetUserByUUID(uuid)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to verify login")
		return
	}
	if user.TwoFactor != "" {
		if strings.TrimSpace(data.TwoFa) == "" {
			api.RespondError(c, http.StatusUnauthorized, "2FA code is required")
			return
		}
		ok, verifyErr := accounts.Verify2Fa(uuid, data.TwoFa)
		if verifyErr != nil {
			api.RespondError(c, http.StatusInternalServerError, "Failed to verify login")
			return
		}
		if !ok {
			accounts.RecordLoginFailure(c.ClientIP(), data.Username)
			api.RespondError(c, http.StatusUnauthorized, "Invalid credentials")
			return
		}
	}
	session, err := accounts.CreateSession(uuid, sessionCookieMaxAge, c.Request.UserAgent(), c.ClientIP(), "password")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to create session: "+err.Error())
		return
	}
	setSessionCookie(c, session, sessionCookieMaxAge)
	accounts.ClearLoginFailures(c.ClientIP(), data.Username)
	auditlog.Log(c.ClientIP(), uuid, "logged in (password)", "login")
	api.RespondSuccess(c, gin.H{})
}
func Logout(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	accounts.DeleteSession(session)
	remotectl.RevokeLogin(session)
	setSessionCookie(c, "", -1)
	auditlog.Log(c.ClientIP(), "", "logged out", "logout")
	c.Redirect(302, "/")
}
