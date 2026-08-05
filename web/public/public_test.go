package public

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRemoveFaviconIfHashMatches(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "favicon.ico")
	legacyData := []byte("legacy default favicon")
	customData := []byte("custom favicon")
	legacyHash := sha256.Sum256(legacyData)

	if err := os.WriteFile(filePath, legacyData, 0644); err != nil {
		t.Fatal(err)
	}
	removed, err := removeFaviconIfHashMatches(filePath, legacyHash)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("legacy default favicon was not removed")
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("legacy favicon still exists: %v", err)
	}

	if err := os.WriteFile(filePath, customData, 0644); err != nil {
		t.Fatal(err)
	}
	removed, err = removeFaviconIfHashMatches(filePath, legacyHash)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("custom favicon was removed")
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(customData) {
		t.Fatalf("custom favicon changed: got %q", got)
	}
}

func TestNormalizeHTMLLanguage(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"hyphen language": {
			input: "zh-CN",
			want:  "zh-CN",
		},
		"underscore language": {
			input: "zh_CN",
			want:  "zh-CN",
		},
		"reject script injection": {
			input: `zh-CN" autofocus`,
		},
		"reject too short": {
			input: "z",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := normalizeHTMLLanguage(tt.input); got != tt.want {
				t.Fatalf("normalizeHTMLLanguage(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestReplaceHTMLLanguage(t *testing.T) {
	tests := map[string]struct {
		html     string
		language string
		want     string
	}{
		"replace existing lang": {
			html:     `<html lang="en"><head></head></html>`,
			language: "zh-CN",
			want:     `<html lang="zh-CN"><head></head></html>`,
		},
		"insert missing lang": {
			html:     `<html><head></head></html>`,
			language: "ja_JP",
			want:     `<html lang="ja-JP"><head></head></html>`,
		},
		"ignore invalid lang": {
			html:     `<html lang="en"><head></head></html>`,
			language: `zh-CN" autofocus`,
			want:     `<html lang="en"><head></head></html>`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := replaceHTMLLanguage(tt.html, tt.language); got != tt.want {
				t.Fatalf("replaceHTMLLanguage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInjectThemeChangeReload(t *testing.T) {
	withBody := injectThemeChangeReload(`<html><body>theme</body></html>`)
	if !strings.Contains(withBody, themeChangeReloadScript+"</body>") {
		t.Fatalf("theme reload listener was not inserted before body close: %q", withBody)
	}
	if got := strings.Count(injectThemeChangeReload(withBody), themeChangeReloadScript); got != 1 {
		t.Fatalf("theme reload listener count = %d, want 1", got)
	}
	withoutBody := injectThemeChangeReload(`<html>theme</html>`)
	if !strings.HasSuffix(withoutBody, themeChangeReloadScript) {
		t.Fatalf("theme reload listener was not appended: %q", withoutBody)
	}
}

func TestInjectCustomHTML(t *testing.T) {
	got := injectCustomHTML(
		`<HTML><HEAD></HEAD><BODY><main></main></BODY></HTML>`,
		`<style data-custom-head></style>`,
		`<div data-custom-body></div>`,
	)
	if !strings.Contains(got, `<style data-custom-head></style></HEAD>`) {
		t.Fatalf("custom Head content was not inserted before the closing tag: %q", got)
	}
	if !strings.Contains(got, `<div data-custom-body></div></BODY>`) {
		t.Fatalf("custom Body content was not inserted before the closing tag: %q", got)
	}
}

func TestRenderPublicDocumentTitle(t *testing.T) {
	tests := map[string]struct {
		html  string
		title string
		want  string
	}{
		"replace legacy title": {
			html:  `<html><head><title>Komari Monitor</title></head><body></body></html>`,
			title: "Nomi",
			want:  `<title>Nomi</title>`,
		},
		"replace title with attributes and whitespace": {
			html:  "<html><head><TITLE data-theme=\"nezha\">\n Komari Monitor \n</TITLE></head><body></body></html>",
			title: "Nomi",
			want:  `<title>Nomi</title>`,
		},
		"insert missing title": {
			html:  `<html><head><meta charset="utf-8"></head><body></body></html>`,
			title: "Nomi",
			want:  `<meta charset="utf-8"><title>Nomi</title></head>`,
		},
		"escape title markup": {
			html:  `<html><head><title>old</title></head><body></body></html>`,
			title: `Nomi </title><script>alert(1)</script>`,
			want:  `<title>Nomi &lt;/title&gt;&lt;script&gt;alert(1)&lt;/script&gt;</title>`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := renderPublicDocumentTitle(tt.html, tt.title)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("renderPublicDocumentTitle() = %q, want fragment %q", got, tt.want)
			}
			if strings.Count(got, documentTitleSyncMarker) != 1 {
				t.Fatalf("title synchronization marker count = %d, want 1", strings.Count(got, documentTitleSyncMarker))
			}
			if strings.Contains(got, `const expectedTitle="Nomi </title>`) {
				t.Fatalf("title was embedded into script without safe escaping: %q", got)
			}
			if rerendered := renderPublicDocumentTitle(got, tt.title); strings.Count(rerendered, documentTitleSyncMarker) != 1 {
				t.Fatalf("title synchronization was injected more than once: %q", rerendered)
			}
		})
	}
}

func TestPublicIndexAlwaysRendersCustomHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	if err := config.SetMany(map[string]any{
		config.CustomHeadKey: `<style data-custom-head>body{--custom-marker:1}</style>`,
		config.CustomBodyKey: `<div data-custom-body>custom body marker</div>`,
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	Static(router.Group("/"), router.NoRoute)

	for _, requestPath := range []string{"/", "/index.html", "/admin", "/terminal"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", requestPath, recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `data-custom-head`) || !strings.Contains(body, `data-custom-body`) {
			t.Fatalf("GET %s bypassed custom HTML rendering", requestPath)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
			t.Fatalf("GET %s Cache-Control = %q", requestPath, got)
		}
	}
}
