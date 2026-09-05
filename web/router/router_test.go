package router

import (
	"bytes"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterDoesNotExposeHTTPSCertificateUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
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
	if !bytes.Contains(source, []byte(`clientGroup.GET("/:uuid/token", api.RequireSensitive2FA(), jsonRpc.Bind("admin:getClientToken"`)) {
		t.Fatal("GET token must require sensitive 2FA")
	}
	if !bytes.Contains(source, []byte(`clientGroup.POST("/token/rotate", api.RequireSensitive2FA(), jsonRpc.Bind("admin:rotateClientToken")`)) {
		t.Fatal("rotate token must require sensitive 2FA")
	}
}

func TestRegisterRemovesLegacyAgentAndTerminalRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
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
}
