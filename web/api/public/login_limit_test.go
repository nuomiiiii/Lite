package public

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginReturnsTooManyRequestsWhenArgon2Busy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts.ResetLoginLimitsForTest()
	username := "busy-login"
	accounts.CreateAccount(username, "correctpassword")
	t.Cleanup(func() {
		accounts.DeleteAccountByUsername(username)
		accounts.DeleteAllSessions()
		accounts.ResetLoginLimitsForTest()
	})

	release, ok := accounts.HoldArgon2ForTest()
	require.True(t, ok)
	defer release()

	router := gin.New()
	router.POST("/login", Login)
	body, _ := json.Marshal(LoginRequest{Username: username, Password: "correctpassword"})
	req, _ := http.NewRequest("POST", "/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "系统繁忙，请稍后重试")
	assert.NotContains(t, w.Body.String(), "Invalid credentials")
}

func TestLoginRateLimitReturnsTooManyRequestsBeforeArgon2(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts.ResetLoginLimitsForTest()
	username := "limit-login"
	accounts.CreateAccount(username, "correctpassword")
	t.Cleanup(func() {
		accounts.DeleteAccountByUsername(username)
		accounts.DeleteAllSessions()
		accounts.ResetLoginLimitsForTest()
	})

	router := gin.New()
	router.POST("/login", Login)
	started := 0
	accounts.SetArgon2VerifyObserverForTest(func() { started++ })
	t.Cleanup(func() { accounts.SetArgon2VerifyObserverForTest(nil) })

	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(LoginRequest{Username: username, Password: "wrongpassword"})
		req, _ := http.NewRequest("POST", "/login", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.20:43000"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	}
	started = 0
	body, _ := json.Marshal(LoginRequest{Username: username, Password: "correctpassword"})
	req, _ := http.NewRequest("POST", "/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.20:43000"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "系统繁忙，请稍后重试")
	assert.Equal(t, 0, started)
}

func TestLoginWrongTOTPTriggersLimitWithoutLeaking(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts.ResetLoginLimitsForTest()
	username := "totp-limit-" + uuid.NewString()[:8]
	user, err := accounts.CreateAccount(username, "correctpassword")
	require.NoError(t, err)
	t.Cleanup(func() {
		accounts.DeleteAccountByUsername(username)
		accounts.DeleteAllSessions()
		accounts.ResetLoginLimitsForTest()
	})

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Lite", AccountName: username})
	require.NoError(t, err)
	setupCode, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err)
	require.NoError(t, accounts.Enable2Fa(user.UUID, key.Secret(), setupCode))
	require.NoError(t, dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", user.UUID).
		Update("two_factor_counter", 0).Error)

	router := gin.New()
	router.POST("/login", Login)
	login := func(twoFa string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(LoginRequest{Username: username, Password: "correctpassword", TwoFa: twoFa})
		req, _ := http.NewRequest("POST", "/login", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.40:43000"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	for i := 0; i < 5; i++ {
		w := login("000000")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "Invalid credentials")
		assert.NotContains(t, w.Body.String(), "2FA")
		assert.NotContains(t, w.Body.String(), "password")
	}
	w := login("000000")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "系统繁忙，请稍后重试")
	assert.NotContains(t, w.Body.String(), "2FA")

	accounts.ResetLoginLimitsForTest()
	for i := 0; i < 4; i++ {
		w := login("000000")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	}
	live, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err)
	w = login(live)
	assert.Equal(t, http.StatusOK, w.Code)

	w = login("000000")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Invalid credentials")
}

func TestLoginPasswordOkThenRequiresTwoFactor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts.ResetLoginLimitsForTest()
	username := "totp-step-" + uuid.NewString()[:8]
	user, err := accounts.CreateAccount(username, "correctpassword")
	require.NoError(t, err)
	t.Cleanup(func() {
		accounts.DeleteAccountByUsername(username)
		accounts.DeleteAllSessions()
		accounts.ResetLoginLimitsForTest()
	})

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Lite", AccountName: username})
	require.NoError(t, err)
	setupCode, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err)
	require.NoError(t, accounts.Enable2Fa(user.UUID, key.Secret(), setupCode))
	require.NoError(t, dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", user.UUID).
		Update("two_factor_counter", 0).Error)

	router := gin.New()
	router.POST("/login", Login)
	login := func(password, twoFa string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(LoginRequest{Username: username, Password: password, TwoFa: twoFa})
		req, _ := http.NewRequest("POST", "/login", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.41:43000"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	wrongPassword := login("wrongpassword", "")
	assert.Equal(t, http.StatusUnauthorized, wrongPassword.Code)
	assert.Contains(t, wrongPassword.Body.String(), "Invalid credentials")
	assert.NotContains(t, wrongPassword.Body.String(), "2FA")

	for i := 0; i < 5; i++ {
		w := login("correctpassword", "")
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Contains(t, w.Body.String(), "2FA code is required")
		assert.NotContains(t, w.Body.String(), "Invalid credentials")
	}

	live, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err)
	w := login("correctpassword", live)
	assert.Equal(t, http.StatusOK, w.Code)
}
