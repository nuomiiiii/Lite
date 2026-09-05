package admin

import (
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/remotectl"
)

type pendingTwoFactor struct {
	secret    string
	expiresAt time.Time
}

var (
	pendingTwoFactorMu sync.Mutex
	pendingTwoFactors  = make(map[string]pendingTwoFactor)
)

func Generate2FA(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	if session == "" {
		api.RespondError(c, 401, "2FA setup requires an administrator session")
		return
	}
	secret, img, err := accounts.Generate2Fa()
	if err != nil {
		api.RespondError(c, 500, "Failed to generate 2FA")
		return
	}
	rememberPendingTwoFactor(session, secret)
	c.Header("Cache-Control", "no-store, private")
	c.Header("Content-Type", "image/png")
	c.Writer.WriteHeader(200)
	_ = png.Encode(c.Writer, img)
}

func Enable2FA(c *gin.Context) {
	uuid, _ := c.Get("uuid")
	userUUID, _ := uuid.(string)
	session, _ := c.Cookie("session_token")
	code := readTwoFactorCode(c)
	secret := takePendingTwoFactor(session)
	if secret == "" || userUUID == "" || code == "" {
		api.RespondError(c, 400, "2FA secret or code not provided")
		return
	}
	if err := accounts.Enable2Fa(userUUID, secret, code); err != nil {
		if errors.Is(err, accounts.ErrTwoFactorInvalid) {
			rememberPendingTwoFactor(session, secret)
			api.RespondError(c, 400, "Invalid 2FA code")
			return
		}
		api.RespondError(c, 500, "Failed to enable 2FA")
		return
	}
	api.RespondSuccess(c, "2FA enabled successfully")
}

func Disable2FA(c *gin.Context) {
	uuid, _ := c.Get("uuid")
	userUUID, _ := uuid.(string)
	err := accounts.Disable2Fa(userUUID)
	if err != nil {
		api.RespondError(c, 500, "Failed to disable 2FA")
		return
	}
	remotectl.RevokeUser(userUUID)
	api.RespondSuccess(c, "")
}

func readTwoFactorCode(c *gin.Context) string {
	if c.Request.Body == nil {
		return ""
	}
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil || len(bodyBytes) == 0 {
		return ""
	}
	var body struct {
		Code  string `json:"code"`
		TwoFA string `json:"2fa_code"`
		OTP   string `json:"otp"`
	}
	if json.Unmarshal(bodyBytes, &body) != nil {
		return ""
	}
	if body.Code != "" {
		return body.Code
	}
	if body.TwoFA != "" {
		return body.TwoFA
	}
	return body.OTP
}

func rememberPendingTwoFactor(session, secret string) {
	if session == "" || secret == "" {
		return
	}
	pendingTwoFactorMu.Lock()
	pendingTwoFactors[session] = pendingTwoFactor{secret: secret, expiresAt: time.Now().Add(30 * time.Minute)}
	pendingTwoFactorMu.Unlock()
}

func takePendingTwoFactor(session string) string {
	if session == "" {
		return ""
	}
	pendingTwoFactorMu.Lock()
	defer pendingTwoFactorMu.Unlock()
	stored, ok := pendingTwoFactors[session]
	if !ok {
		return ""
	}
	delete(pendingTwoFactors, session)
	if time.Now().After(stored.expiresAt) {
		return ""
	}
	return stored.secret
}
