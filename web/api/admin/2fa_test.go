package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/utils/instancekey"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/pquerna/otp/totp"
)

func TestMain(m *testing.M) {
	cleanup := instancekey.SetupTempFileForTest()
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:admin_api_test?mode=memory&cache=shared"
	db := dbcore.GetDBInstance()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func TestTwoFactorEndpointsRejectOverwriteWithConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	username := "2fa-" + uuid.NewString()[:8]
	user := models.User{
		UUID:     uuid.NewString(),
		Username: username,
		Passwd:   "unused-in-test",
	}
	if err := dbcore.GetDBInstance().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbcore.GetDBInstance().Where("uuid = ?", user.UUID).Delete(&models.User{}).Error })

	sessionTok := "sess-" + uuid.NewString()

	// First-time enrollment succeeds.
	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	req1 := httptest.NewRequest(http.MethodGet, "/api/admin/2fa/generate", nil)
	req1.AddCookie(&http.Cookie{Name: "session_token", Value: sessionTok})
	c1.Request = req1
	c1.Set("uuid", user.UUID)
	Generate2FA(c1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first Generate2FA status = %d, body = %s", w1.Code, w1.Body.String())
	}

	pendingTwoFactorMu.Lock()
	secret1 := pendingTwoFactors[sessionTok].secret
	pendingTwoFactorMu.Unlock()
	code1, err := totp.GenerateCode(secret1, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	req2 := httptest.NewRequest(http.MethodPost, "/api/admin/2fa/enable", bytes.NewBufferString(`{"code":"`+code1+`"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(&http.Cookie{Name: "session_token", Value: sessionTok})
	c2.Request = req2
	c2.Set("uuid", user.UUID)
	Enable2FA(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("first Enable2FA status = %d, body = %s", w2.Code, w2.Body.String())
	}

	// Subsequent Generate2FA while 2FA is active must return 409 Conflict.
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	req3 := httptest.NewRequest(http.MethodGet, "/api/admin/2fa/generate", nil)
	req3.AddCookie(&http.Cookie{Name: "session_token", Value: sessionTok})
	c3.Request = req3
	c3.Set("uuid", user.UUID)
	Generate2FA(c3)
	if w3.Code != http.StatusConflict || !strings.Contains(w3.Body.String(), "2FA is already enabled; disable it first") {
		t.Fatalf("second Generate2FA status = %d, body = %s", w3.Code, w3.Body.String())
	}

	// Even if a pending secret existed in memory, Enable2FA must return 409 Conflict.
	secret2, _, err := accounts.Generate2Fa()
	if err != nil {
		t.Fatal(err)
	}
	rememberPendingTwoFactor(sessionTok, secret2)
	code2, err := totp.GenerateCode(secret2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(w4)
	req4 := httptest.NewRequest(http.MethodPost, "/api/admin/2fa/enable", bytes.NewBufferString(`{"code":"`+code2+`"}`))
	req4.Header.Set("Content-Type", "application/json")
	req4.AddCookie(&http.Cookie{Name: "session_token", Value: sessionTok})
	c4.Request = req4
	c4.Set("uuid", user.UUID)
	Enable2FA(c4)
	if w4.Code != http.StatusConflict || !strings.Contains(w4.Body.String(), "2FA is already enabled; disable it first") {
		t.Fatalf("second Enable2FA status = %d, body = %s", w4.Code, w4.Body.String())
	}
}

func TestCrossSiteNavigationRejectedOnBindAndSetTheme(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	req1 := httptest.NewRequest(http.MethodGet, "/api/admin/oauth2/bind", nil)
	req1.Header.Set("Sec-Fetch-Site", "cross-site")
	c1.Request = req1
	BindingExternalAccount(c1)
	if w1.Code != http.StatusForbidden {
		t.Fatalf("BindingExternalAccount cross-site status = %d, want 403", w1.Code)
	}

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/theme/set?theme=default", nil)
	req2.Header.Set("Sec-Fetch-Site", "cross-site")
	c2.Request = req2
	SetTheme(c2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("SetTheme cross-site status = %d, want 403", w2.Code)
	}
}

func TestTwoFactorRevokesOtherSessionsAndOrphanedSessionDeniedOnREST(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := models.User{
		UUID:     uuid.NewString(),
		Username: "2fa-hygiene-" + uuid.NewString()[:8],
		Passwd:   "unused-in-test",
	}
	if err := dbcore.GetDBInstance().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = dbcore.GetDBInstance().Where("uuid = ?", user.UUID).Delete(&models.Session{}).Error
		_ = dbcore.GetDBInstance().Where("uuid = ?", user.UUID).Delete(&models.User{}).Error
	})

	sessCurrent, err := accounts.CreateSession(user.UUID, 3600, "ua-1", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}
	sessOther, err := accounts.CreateSession(user.UUID, 3600, "ua-2", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}

	secret, _, err := accounts.Generate2Fa()
	if err != nil {
		t.Fatal(err)
	}
	rememberPendingTwoFactor(sessCurrent, secret)
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	wEn := httptest.NewRecorder()
	cEn, _ := gin.CreateTestContext(wEn)
	reqEn := httptest.NewRequest(http.MethodPost, "/api/admin/2fa/enable", bytes.NewBufferString(`{"code":"`+code+`"}`))
	reqEn.Header.Set("Content-Type", "application/json")
	reqEn.AddCookie(&http.Cookie{Name: "session_token", Value: sessCurrent})
	cEn.Request = reqEn
	cEn.Set("uuid", user.UUID)
	Enable2FA(cEn)
	if wEn.Code != http.StatusOK {
		t.Fatalf("Enable2FA status = %d, body = %s", wEn.Code, wEn.Body.String())
	}
	if !accounts.SessionStillValid(user.UUID, sessCurrent) {
		t.Fatal("current session must remain valid after Enable2FA")
	}
	if accounts.SessionStillValid(user.UUID, sessOther) {
		t.Fatal("other session must be revoked after Enable2FA")
	}

	// Delete user row while keeping sessCurrent in sessions table -> orphaned session must NOT get RoleAdmin on Gin REST routes.
	if err := dbcore.GetDBInstance().Where("uuid = ?", user.UUID).Delete(&models.User{}).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(api.IdentityMiddleware())
	router.GET("/api/admin/probe", api.RequireRole(api.RoleAdmin), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	wOrphan := httptest.NewRecorder()
	reqOrphan := httptest.NewRequest(http.MethodGet, "/api/admin/probe", nil)
	reqOrphan.AddCookie(&http.Cookie{Name: "session_token", Value: sessCurrent})
	router.ServeHTTP(wOrphan, reqOrphan)
	if wOrphan.Code != http.StatusUnauthorized {
		t.Fatalf("orphaned session of deleted user got status %d, want 401", wOrphan.Code)
	}
}
