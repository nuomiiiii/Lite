package public

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/passkey"
)

func passkeyOptionsThrottled(c *gin.Context) bool {
	ip := c.ClientIP()
	return accounts.LoginThrottled(ip, accounts.PasskeyThrottleBucket) ||
		accounts.LoginRequestThrottled(ip, accounts.PasskeyOptionsBucket, accounts.PasskeyOptionsMaxRequests)
}

func passkeyVerifyThrottled(c *gin.Context) bool {
	return accounts.LoginThrottled(c.ClientIP(), accounts.PasskeyThrottleBucket)
}

func recordPasskeyVerifyFailure(c *gin.Context) {
	accounts.RecordLoginFailure(c.ClientIP(), accounts.PasskeyThrottleBucket)
}

func PasskeyLoginOptions(c *gin.Context) {
	if passkeyOptionsThrottled(c) {
		api.RespondError(c, http.StatusTooManyRequests, accounts.ErrPasswordBusy.Error())
		return
	}
	accounts.RecordLoginRequest(c.ClientIP(), accounts.PasskeyOptionsBucket)
	wa, err := passkey.NewWebAuthn(c)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to start passkey login: "+err.Error())
		return
	}
	assertion, session, err := wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to start passkey login: "+err.Error())
		return
	}
	ceremonyID := passkey.PutCeremony(passkey.CeremonyLogin, "", "", session)
	api.RespondSuccess(c, gin.H{
		"ceremony_id": ceremonyID,
		"publicKey":   assertion.Response,
	})
}

func PasskeyLoginVerify(c *gin.Context) {
	if passkeyVerifyThrottled(c) {
		api.RespondError(c, http.StatusTooManyRequests, accounts.ErrPasswordBusy.Error())
		return
	}
	var req struct {
		CeremonyID string          `json:"ceremony_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CeremonyID == "" || len(req.Credential) == 0 {
		recordPasskeyVerifyFailure(c)
		api.RespondError(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	ceremony, err := passkey.TakeCeremony(req.CeremonyID, passkey.CeremonyLogin)
	if err != nil {
		recordPasskeyVerifyFailure(c)
		api.RespondError(c, http.StatusBadRequest, "Passkey login expired")
		return
	}
	wa, err := passkey.NewWebAuthn(c)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to verify passkey")
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(req.Credential))
	if err != nil {
		recordPasskeyVerifyFailure(c)
		api.RespondError(c, http.StatusBadRequest, "Invalid authenticator response")
		return
	}
	user, cred, err := wa.ValidatePasskeyLogin(passkey.LoadUserByHandle, ceremony.Session, parsed)
	if err != nil {
		recordPasskeyVerifyFailure(c)
		api.RespondError(c, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	userUUID := passkey.UserUUIDFromHandle(user.WebAuthnID())
	if err := passkey.UpdateCredential(userUUID, cred); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to update passkey")
		return
	}
	session, err := accounts.CreateSession(userUUID, sessionCookieMaxAgeSeconds(), c.Request.UserAgent(), c.ClientIP(), "passkey")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to create session: "+err.Error())
		return
	}
	setSessionCookie(c, session, sessionCookieMaxAgeSeconds())
	accounts.ClearLoginFailures(c.ClientIP(), accounts.PasskeyThrottleBucket)
	accounts.ClearLoginFailures(c.ClientIP(), accounts.PasskeyOptionsBucket)
	auditlog.Event(c.ClientIP(), userUUID, "login", "audit.login_passkey", nil)
	api.RespondSuccess(c, gin.H{})
}
