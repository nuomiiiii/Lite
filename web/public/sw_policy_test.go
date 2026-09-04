package public

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRootServiceWorkerEntriesAreNotThemeOwned(t *testing.T) {
	t.Chdir(t.TempDir())
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	themeDir := filepath.Join("data", "theme", "lite-theme", "dist")
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "sw.js"), []byte("theme-sw"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "hijack.js"), []byte("register()"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("data", "theme", "lite-theme", "Lite-theme.json"), []byte(`{"name":"lite-theme"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(config.ThemeKey, "lite-theme"); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	Static(router.Group("/"), router.NoRoute)

	for _, path := range []string{"/sw.js", "/service-worker.js", "/registersw.js"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status=%d body=%q", path, recorder.Code, recorder.Body.String())
		}
		if recorder.Body.String() == "theme-sw" {
			t.Fatalf("GET %s returned theme worker content", path)
		}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hijack.js", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("theme script status=%d", recorder.Code)
	}
	if got := recorder.Header().Get("Service-Worker-Allowed"); got != themeServiceWorkerAllowed {
		t.Fatalf("Service-Worker-Allowed=%q", got)
	}
}

func TestRootServiceWorkerPathDetection(t *testing.T) {
	if !isRootServiceWorkerPath("/sw.js") || !isRootServiceWorkerPath("/service-worker.js") {
		t.Fatal("root worker paths must be recognized")
	}
	if isRootServiceWorkerPath("/assets/sw.js") || isRootServiceWorkerPath("/admin") {
		t.Fatal("non-root paths must not be treated as root workers")
	}
}
