package public

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPrivateApplicationPaths(t *testing.T) {
	for _, path := range []string{"/admin", "/admin/settings", "/terminal", "/terminal/node-a"} {
		if !isPrivateApplicationPath(path) {
			t.Fatalf("private path %q was not recognized", path)
		}
	}
	for _, path := range []string{"/", "/administrator", "/terminal-status", "/assets/admin.js"} {
		if isPrivateApplicationPath(path) {
			t.Fatalf("public path %q was treated as private", path)
		}
	}
}

func TestStaticCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		path string
		want string
	}{
		{"/sw.js", "no-store, no-cache, must-revalidate"},
		{"/service-worker.js", "no-store, no-cache, must-revalidate"},
		{"/manifest.json", "no-store, no-cache, must-revalidate"},
		{"/manifest.webmanifest", "no-store, no-cache, must-revalidate"},
		{"/assets/entry-main-abcdef.js", "public, max-age=31536000, immutable"},
		{"/assets/logo.png", ""},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		setStaticCacheHeaders(context, test.path)
		if got := recorder.Header().Get("Cache-Control"); got != test.want {
			t.Fatalf("Cache-Control for %q=%q, want %q", test.path, got, test.want)
		}
	}
}

func TestThemeStaticCacheHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	setThemeStaticCacheHeaders(context, "/themes/glass/preview.png")
	if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("theme image Cache-Control=%q", got)
	}
}
