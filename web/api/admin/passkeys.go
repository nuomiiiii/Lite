package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/passkey"
)

func currentUserUUID(c *gin.Context) string {
	raw, _ := c.Get("uuid")
	uuidStr, _ := raw.(string)
	return uuidStr
}

func ListPasskeys(c *gin.Context) {
	uuidStr := currentUserUUID(c)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	items, err := passkey.Summaries(uuidStr)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to list passkeys")
		return
	}
	api.RespondSuccess(c, items)
}

func PasskeyConfirmOptions(c *gin.Context) {
	uuidStr := currentUserUUID(c)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	ceremonyID, publicKey, err := passkey.BeginUserAssertion(c, uuidStr)
	if err != nil {
		if errors.Is(err, passkey.ErrNoPasskeys) {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		api.RespondError(c, http.StatusInternalServerError, "Failed to start passkey confirmation")
		return
	}
	api.RespondSuccess(c, gin.H{
		"ceremony_id": ceremonyID,
		"publicKey":   publicKey,
	})
}

func PasskeyRegisterOptions(c *gin.Context) {
	uuidStr := currentUserUUID(c)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	var req struct {
		Name       string          `json:"name"`
		Password   string          `json:"password"`
		TwoFa      string          `json:"2fa_code"`
		Method     string          `json:"method"`
		Prefer     string          `json:"prefer"`
		CeremonyID string          `json:"ceremony_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		api.RespondError(c, http.StatusBadRequest, "Passkey name is required")
		return
	}
	if err := authorizePasskeyRegister(c, uuidStr, req.Method, req.Password, req.TwoFa, req.CeremonyID, req.Credential); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, passkey.ErrNoPasskeys) || errors.Is(err, errInvalidPasskeyMethod) {
			status = http.StatusBadRequest
		}
		if accounts.IsPasswordBusy(err) {
			status = http.StatusTooManyRequests
		}
		api.RespondError(c, status, err.Error())
		return
	}
	wa, err := passkey.NewWebAuthn(c)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to start passkey registration")
		return
	}
	user, err := passkey.LoadUser(uuidStr)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to start passkey registration")
		return
	}
	selection := protocol.AuthenticatorSelection{
		ResidentKey:        protocol.ResidentKeyRequirementRequired,
		RequireResidentKey: protocol.ResidentKeyRequired(),
		UserVerification:   protocol.VerificationRequired,
	}
	opts := []webauthn.RegistrationOption{
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
	}
	if strings.TrimSpace(req.Prefer) == "password-manager" {
		// Leave attachment and hints unset. hybrid/client-device would make
		// go-webauthn infer cross-platform or platform, which hides password
		// managers on iOS or forces Windows Hello on desktop.
		opts = append(opts, webauthn.WithAuthenticatorSelection(selection))
	} else {
		selection.AuthenticatorAttachment = protocol.Platform
		opts = append(opts,
			webauthn.WithAuthenticatorSelection(selection),
			webauthn.WithPublicKeyCredentialHints([]protocol.PublicKeyCredentialHints{
				protocol.PublicKeyCredentialHintClientDevice,
			}),
		)
	}
	creation, session, err := wa.BeginRegistration(user, opts...)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	ceremonyID := passkey.PutCeremony(passkey.CeremonyRegister, uuidStr, req.Name, session)
	api.RespondSuccess(c, gin.H{
		"ceremony_id": ceremonyID,
		"publicKey":   creation.Response,
	})
}

func PasskeyRegisterVerify(c *gin.Context) {
	uuidStr := currentUserUUID(c)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	var req struct {
		CeremonyID string          `json:"ceremony_id"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.CeremonyID == "" || len(req.Credential) == 0 {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	ceremony, err := passkey.TakeCeremony(req.CeremonyID, passkey.CeremonyRegister)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Passkey registration expired")
		return
	}
	if ceremony.UserUUID != uuidStr {
		api.RespondError(c, http.StatusForbidden, "Passkey registration does not belong to this account")
		return
	}
	wa, err := passkey.NewWebAuthn(c)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to verify passkey")
		return
	}
	user, err := passkey.LoadUser(uuidStr)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to verify passkey")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(req.Credential))
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid authenticator response")
		return
	}
	cred, err := wa.CreateCredential(user, ceremony.Session, parsed)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Failed to verify passkey")
		return
	}
	summary, err := passkey.SaveCredential(uuidStr, wa.Config.RPID, ceremony.Name, c.Request.UserAgent(), cred)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to save passkey")
		return
	}
	auditlog.Event(c.ClientIP(), uuidStr, "info", "audit.passkey_add", map[string]string{"name": summary.Name})
	api.RespondSuccess(c, summary)
}

func RenamePasskey(c *gin.Context) {
	uuidStr := currentUserUUID(c)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := passkey.RenameCredential(uuidStr, c.Param("id"), req.Name); err != nil {
		if errors.Is(err, passkey.ErrPasskeyName) {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, passkey.ErrPasskeyNotFound) {
			api.RespondError(c, http.StatusNotFound, err.Error())
			return
		}
		api.RespondError(c, http.StatusInternalServerError, "Failed to rename passkey")
		return
	}
	api.RespondSuccess(c, nil)
}

func DeletePasskey(c *gin.Context) {
	uuidStr := currentUserUUID(c)
	if uuidStr == "" {
		api.RespondError(c, http.StatusUnauthorized, "Unauthorized.")
		return
	}
	if err := passkey.DeleteCredential(uuidStr, c.Param("id")); err != nil {
		if errors.Is(err, passkey.ErrLastLoginMethod) {
			api.RespondError(c, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, passkey.ErrPasskeyNotFound) {
			api.RespondError(c, http.StatusNotFound, err.Error())
			return
		}
		api.RespondError(c, http.StatusInternalServerError, "Failed to delete passkey")
		return
	}
	auditlog.Event(c.ClientIP(), uuidStr, "warn", "audit.passkey_remove", nil)
	api.RespondSuccess(c, nil)
}

var (
	errInvalidPasskeyMethod = errors.New("invalid passkey confirmation method")
	errSSOConfirmRequired   = errors.New("SSO confirmation expired")
	errInvalidCredentials   = errors.New("Invalid credentials")
)

func authorizePasskeyRegister(c *gin.Context, uuidStr, method, password, twoFa, ceremonyID string, credential json.RawMessage) error {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "", "password":
		if err := accounts.VerifyPasswordForUUID(uuidStr, password); err != nil {
			if accounts.IsPasswordBusy(err) {
				return err
			}
			return errInvalidCredentials
		}
		c.Set("2fa_code", twoFa)
		return api.VerifySensitive2FA(c)
	case "passkey":
		if err := passkey.FinishUserAssertion(c, uuidStr, ceremonyID, credential); err != nil {
			if errors.Is(err, passkey.ErrNoPasskeys) || errors.Is(err, passkey.ErrConfirmNotFound) || errors.Is(err, passkey.ErrAssertionMismatch) {
				return err
			}
			return errInvalidCredentials
		}
		return nil
	case "sso":
		return consumeSSOConfirm(c, uuidStr)
	default:
		return errInvalidPasskeyMethod
	}
}

func consumeSSOConfirm(c *gin.Context, uuidStr string) error {
	token, err := c.Cookie("passkey_confirm_ok")
	c.SetCookie("passkey_confirm_ok", "", -1, "/", "", false, true)
	if err != nil || token == "" {
		return errSSOConfirmRequired
	}
	grant, err := passkey.TakeConfirmGrant(token)
	if err != nil || grant.UserUUID != uuidStr {
		return errSSOConfirmRequired
	}
	return nil
}
