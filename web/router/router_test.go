package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils/instancekey"
	"github.com/nuomiiiii/lite/web/api"
)

func TestMain(m *testing.M) {
	cleanup := instancekey.SetupTempFileForTest()
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:router_pkg_test?mode=memory&cache=shared"
	db := dbcore.GetDBInstance()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func TestRegisterDoesNotExposeHTTPSCertificateUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(api.IdentityMiddleware())
	Register(engine)

	for _, route := range engine.Routes() {
		if route.Path == "/api/admin/settings/https/upload" {
			t.Fatalf("removed HTTPS certificate upload route is still registered: %s %s", route.Method, route.Path)
		}
	}
}

func TestRegisterUsesOnlyChunkedArchiveUploadRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(api.IdentityMiddleware())
	Register(engine)

	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /api/admin/upload/init",
		"POST /api/admin/upload/chunk",
		"POST /api/admin/upload/merge",
		"POST /api/admin/upload/cancel",
	} {
		if !routes[route] {
			t.Fatalf("chunked upload route is missing: %s", route)
		}
	}
	for _, route := range []string{
		"POST /api/admin/upload/backup",
		"PUT /api/admin/theme/upload",
	} {
		if routes[route] {
			t.Fatalf("retired direct upload route is still registered: %s", route)
		}
	}
}

func TestRegisterProtectsClientTokenRoutesWithSensitive2FA(t *testing.T) {
	source, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte(`clientGroup.GET("/:uuid/token", api.RejectAPIKey(), api.RequireSensitive2FA(), jsonRpc.Bind("admin:getClientToken"`)) {
		t.Fatal("GET token must require a human session and sensitive 2FA")
	}
	if !bytes.Contains(source, []byte(`clientGroup.POST("/:uuid/token", api.RejectAPIKey(), api.RequireSensitive2FA(), jsonRpc.Bind("admin:getClientToken"`)) {
		t.Fatal("POST token must require a human session and sensitive 2FA so passkeys can replace OTP")
	}
	if !bytes.Contains(source, []byte(`clientGroup.POST("/token/rotate", api.RejectAPIKey(), api.RequireSensitive2FA(), jsonRpc.Bind("admin:rotateClientToken")`)) {
		t.Fatal("rotate token must require a human session and sensitive 2FA")
	}
	if !bytes.Contains(source, []byte(`theme.Use(api.RejectAPIKey())`)) {
		t.Fatal("theme APIs must reject API keys")
	}
	if !bytes.Contains(source, []byte(`g.GET("/download/backup", api.RejectAPIKey(), admin.DownloadBackup)`)) {
		t.Fatal("backup download must reject API keys")
	}
	if !bytes.Contains(source, []byte(`uploadGroup.Use(api.RejectAPIKey())`)) {
		t.Fatal("archive upload must reject API keys")
	}
	if !bytes.Contains(source, []byte(`g.POST("/update/user", api.RejectAPIKey(), admin.UpdateUser)`)) {
		t.Fatal("user update must reject API keys")
	}
	if !bytes.Contains(source, []byte("account.Use(api.RejectAPIKey())")) {
		t.Fatal("account passkey APIs must reject API keys")
	}
	if !bytes.Contains(source, []byte("twoFactor.Use(api.RejectAPIKey())")) {
		t.Fatal("2FA APIs must reject API keys")
	}
	if !bytes.Contains(source, []byte("oauth2.Use(api.RejectAPIKey())")) {
		t.Fatal("SSO bind/unbind APIs must reject API keys")
	}
}

func TestHTTPSSettingsRoutesRejectAPIKeyRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config.SetDb(dbcore.GetDBInstance())
	const testKey = "test-site-api-key-123456"
	if err := config.Set(config.ApiKeyKey, testKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.Set(config.ApiKeyKey, "") })

	engine := gin.New()
	engine.Use(api.IdentityMiddleware())
	Register(engine)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/admin/settings/https"},
		{http.MethodPost, "/api/admin/settings/https"},
		{http.MethodPost, "/api/admin/settings/https/reload"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s with API key returned HTTP %d (%s), want %d", tc.method, tc.path, rec.Code, rec.Body.String(), http.StatusForbidden)
		}
	}
}

func TestRegisterRemovesLegacyAgentAndTerminalRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(api.IdentityMiddleware())
	Register(engine)

	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /api/clients/report",
		"POST /api/clients/uploadBasicInfo",
		"GET /api/clients/terminal",
		"GET /api/admin/client/:uuid/terminal",
	} {
		if routes[route] {
			t.Fatalf("removed route is still registered: %s", route)
		}
	}
	if !routes["GET /api/clients/v2/rpc"] || !routes["POST /api/clients/v2/rpc"] || !routes["GET /api/clients/remote"] {
		t.Fatal("current v2 and remote agent routes are missing")
	}
	if !routes["POST /api/admin/client/remote/authorize"] || !routes["POST /api/admin/client/remote/revoke"] {
		t.Fatal("remote grant routes are missing")
	}
	if !routes["POST /api/admin/account/passkeys/confirm/options"] || !routes["GET /api/admin/oauth2/confirm-passkey"] {
		t.Fatal("passkey confirmation routes are missing")
	}
}

func TestRegisterDoesNotExposeAutoDiscoveryRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(api.IdentityMiddleware())
	Register(engine)

	for _, route := range engine.Routes() {
		if route.Path == "/api/clients/register" || route.Path == "/clients/register" {
			t.Fatalf("auto-discovery register route is still registered: %s %s", route.Method, route.Path)
		}
	}
}
