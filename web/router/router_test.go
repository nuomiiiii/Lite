package router

import (
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
