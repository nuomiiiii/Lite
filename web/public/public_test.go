package public

import (
	"crypto/sha256"
	"encoding/json"
	"io/fs"
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

func TestStaticRendersPublicManifestFromLiveSiteBranding(t *testing.T) {
	t.Chdir(t.TempDir())
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)

	themeDir := filepath.Join(DataDir, ThemesDir, "third-party")
	if err := os.MkdirAll(filepath.Join(themeDir, DistDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"komari-theme.json":                     `{"short":"third-party"}`,
		filepath.Join(DistDir, IndexFile):       `<html><head><title>Theme</title></head><body></body></html>`,
		filepath.Join(DistDir, "manifest.json"): `{"name":"Theme Brand","short_name":"Theme","start_url":"/dashboard","display":"fullscreen","theme_color":"#123456","icons":[{"src":"/theme-icon.png"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(themeDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.SetMany(map[string]any{
		config.ThemeKey:       "third-party",
		config.SitenameKey:    `Komari "Home"`,
		config.DescriptionKey: "Live site description",
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	Static(router.Group("/"), router.NoRoute)
	requestManifest := func(requestPath string) map[string]any {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", requestPath, recorder.Code, http.StatusOK)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/manifest+json") {
			t.Fatalf("GET %s content type = %q", requestPath, got)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
			t.Fatalf("GET %s Cache-Control = %q", requestPath, got)
		}
		var manifest map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		return manifest
	}

	manifest := requestManifest("/manifest.json")
	for key, want := range map[string]string{
		"name":        `Komari "Home"`,
		"short_name":  `Komari "Home"`,
		"description": "Live site description",
		"start_url":   "/dashboard",
		"display":     "fullscreen",
		"theme_color": "#123456",
	} {
		if got := manifest[key]; got != want {
			t.Fatalf("manifest %s = %#v, want %q", key, got, want)
		}
	}
	icons, ok := manifest["icons"].([]any)
	if !ok || len(icons) != 1 {
		t.Fatalf("manifest icons = %#v", manifest["icons"])
	}
	icon, ok := icons[0].(map[string]any)
	if !ok || icon["src"] != "/favicon.ico" || icon["sizes"] != "any" {
		t.Fatalf("manifest icon = %#v", icons[0])
	}

	if err := config.Set(config.SitenameKey, "Renamed live"); err != nil {
		t.Fatal(err)
	}
	if got := requestManifest("/manifest.webmanifest")["name"]; got != "Renamed live" {
		t.Fatalf("updated manifest name = %#v", got)
	}
}

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

func TestCustomHTMLIsLimitedToPublicPages(t *testing.T) {
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

	tests := []struct {
		path       string
		wantCustom bool
	}{
		{path: "/", wantCustom: true},
		{path: "/index.html", wantCustom: true},
		{path: "/admin"},
		{path: "/admin/settings"},
		{path: "/terminal"},
		{path: "/terminal/session"},
		{path: "/install"},
		{path: "/manage"},
	}

	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, tt.path, nil)
		router.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", tt.path, recorder.Code, http.StatusOK)
		}
		body := recorder.Body.String()
		hasCustomHead := strings.Contains(body, `data-custom-head`)
		hasCustomBody := strings.Contains(body, `data-custom-body`)
		if hasCustomHead != tt.wantCustom || hasCustomBody != tt.wantCustom {
			t.Fatalf("GET %s custom HTML = (head: %t, body: %t), want both %t", tt.path, hasCustomHead, hasCustomBody, tt.wantCustom)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
			t.Fatalf("GET %s Cache-Control = %q", tt.path, got)
		}
	}
}

func TestEnsureBundledThemesUsesNezhaForNewInstall(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)

	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	active, err := config.GetAs[string](config.ThemeKey)
	if err != nil {
		t.Fatal(err)
	}
	if active != DefaultTheme {
		t.Fatalf("active theme = %q, want %q", active, DefaultTheme)
	}
	if !IsLocalThemeUsable(DefaultTheme) {
		t.Fatal("bundled Nezha theme was not installed")
	}
}

func TestEnsureBundledThemesMigratesLegacyDefaultToNezha(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	if err := config.Set(config.ThemeKey, LegacyDefaultTheme); err != nil {
		t.Fatal(err)
	}

	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	active, err := config.GetAs[string](config.ThemeKey)
	if err != nil {
		t.Fatal(err)
	}
	if active != DefaultTheme {
		t.Fatalf("active theme = %q, want %q", active, DefaultTheme)
	}
	if !IsLocalThemeUsable(DefaultTheme) {
		t.Fatal("legacy migration did not install the bundled Nezha theme")
	}
	if IsLocalThemeUsable("komari-classic") {
		t.Fatal("legacy migration unexpectedly installed the independent Classic theme")
	}
}

func TestEnsureBundledThemesRepairsRestoreWithoutThemeFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	if err := config.SetMany(map[string]any{
		config.ThemeKey:         "missing-after-restore",
		themeBundleMigrationKey: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	active, err := config.GetAs[string](config.ThemeKey)
	if err != nil {
		t.Fatal(err)
	}
	if active != DefaultTheme || !IsLocalThemeUsable(DefaultTheme) {
		t.Fatalf("restored theme state = %q usable=%t", active, IsLocalThemeUsable(DefaultTheme))
	}
}

func TestEnsureBundledThemesRefreshesExistingNezhaOnce(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)

	staleDir := filepath.Join(DataDir, ThemesDir, DefaultTheme, DistDir)
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(DataDir, ThemesDir, DefaultTheme, "komari-theme.json"), []byte(`{"short":"nezha"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, IndexFile), []byte("stale-theme-index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.SetMany(map[string]any{
		config.ThemeKey:         DefaultTheme,
		themeBundleMigrationKey: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(staleDir, IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(index) == "stale-theme-index" {
		t.Fatal("existing Nezha theme was not refreshed")
	}
	migration, err := config.GetAs[int](themeBundleMigrationKey, 0)
	if err != nil || migration != currentThemeBundleMigration {
		t.Fatalf("theme bundle migration = %d, err=%v", migration, err)
	}

	if err := os.WriteFile(filepath.Join(staleDir, IndexFile), []byte("user-updated-after-migration"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	index, err = os.ReadFile(filepath.Join(staleDir, IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(index) != "user-updated-after-migration" {
		t.Fatal("completed migration overwrote a later user theme update")
	}
}

func TestEnsureBundledThemesDoesNotReinstallDeletedNezha(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)

	customDir := filepath.Join(DataDir, ThemesDir, "third-party", DistDir)
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(DataDir, ThemesDir, "third-party", "komari-theme.json"), []byte(`{"short":"third-party"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, IndexFile), []byte("third-party-theme"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.SetMany(map[string]any{
		config.ThemeKey:         "third-party",
		themeBundleMigrationKey: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	if IsLocalThemeUsable(DefaultTheme) {
		t.Fatal("deleted Nezha theme was reinstalled for a third-party active theme")
	}
	active, err := config.GetAs[string](config.ThemeKey)
	if err != nil || active != "third-party" {
		t.Fatalf("active theme = %q, err=%v", active, err)
	}
}

func TestStaticKeepsSystemUIAndPublicThemeResourcesIsolated(t *testing.T) {
	t.Chdir(t.TempDir())
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	if err := config.SetMany(map[string]any{
		config.ThemeKey:      "missing-theme",
		config.SitenameKey:   "Public Branded Site",
		config.CustomHeadKey: `<meta data-public-custom-head>`,
		config.CustomBodyKey: `<div data-public-custom-body></div>`,
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	Static(router.Group("/"), router.NoRoute)

	request := func(requestPath string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
		return recorder
	}

	publicPage := request("/")
	if publicPage.Code != http.StatusOK || !strings.Contains(publicPage.Body.String(), "data-public-custom-head") {
		t.Fatalf("public rescue page status=%d body=%q", publicPage.Code, publicPage.Body.String())
	}
	if !strings.Contains(publicPage.Body.String(), "font-logos") {
		t.Fatal("missing public theme did not use the embedded Nezha rescue page")
	}

	adminPage := request("/admin/settings/theme")
	if adminPage.Code != http.StatusOK {
		t.Fatalf("system UI status=%d", adminPage.Code)
	}
	if !strings.Contains(adminPage.Body.String(), "/system-assets/") {
		t.Fatal("system UI did not reference its independent asset prefix")
	}
	if strings.Contains(adminPage.Body.String(), "data-public-custom-head") || strings.Contains(adminPage.Body.String(), "font-logos") {
		t.Fatal("public theme content leaked into the system UI")
	}

	entries, err := fs.Glob(PublicFS, "systemUI/dist/assets/entry-*.js")
	if err != nil || len(entries) == 0 {
		t.Fatalf("find embedded system UI entry: %v", err)
	}
	assetPath := "/system-assets/" + strings.TrimPrefix(entries[0], "systemUI/dist/")
	if asset := request(assetPath); asset.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d", assetPath, asset.Code)
	}
	systemManifest := request("/system-assets/manifest.json")
	if systemManifest.Code != http.StatusOK {
		t.Fatalf("system manifest status=%d body=%q", systemManifest.Code, systemManifest.Body.String())
	}
	if got := systemManifest.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
		t.Fatalf("system manifest Cache-Control=%q", got)
	}
	var systemManifestData map[string]any
	if err := json.Unmarshal(systemManifest.Body.Bytes(), &systemManifestData); err != nil {
		t.Fatalf("decode system manifest: %v", err)
	}
	if systemManifestData["name"] != "Public Branded Site" || systemManifestData["theme_color"] != "#2563eb" {
		t.Fatalf("system manifest=%#v", systemManifestData)
	}
	if favicon := request("/favicon.ico"); favicon.Code != http.StatusOK || favicon.Header().Get("Content-Type") != "image/x-icon" {
		t.Fatalf("system favicon fallback status=%d content-type=%q", favicon.Code, favicon.Header().Get("Content-Type"))
	}
	for _, missing := range []string{
		"/system-assets/assets/not-present.js",
		"/themes/nezha/dist/not-present.js",
		"/assets/not-present.js",
	} {
		if response := request(missing); response.Code != http.StatusNotFound {
			t.Fatalf("GET %s status=%d, want 404", missing, response.Code)
		}
	}
}
