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
	"github.com/nuomiiiii/lite/pkg/config"
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
			html:  `<html><head><title>Lite</title></head><body></body></html>`,
			title: "Nomi",
			want:  `<title>Nomi</title>`,
		},
		"replace title with attributes and whitespace": {
			html:  "<html><head><TITLE data-theme=\"nezha\">\n Lite \n</TITLE></head><body></body></html>",
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

func TestRenderApplicationIdentityUsesBackendNameAndFavicon(t *testing.T) {
	htmlStr := `<html><head>
<title>Theme title</title>
<meta name="apple-mobile-web-app-title" content="Theme application" />
<link rel="shortcut icon" href="relative-favicon.ico" />
<link rel="icon" type="image/png" href="/theme-icon.png" />
<link rel="apple-touch-icon" href="/theme-touch-icon.png" />
</head><body></body></html>`

	got := renderApplicationIdentity(htmlStr, `Nomi & Friends`)
	for _, want := range []string{
		`<title>Nomi &amp; Friends</title>`,
		`<meta name="apple-mobile-web-app-title" content="Nomi &amp; Friends" />`,
		`<meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover" />`,
		`<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />`,
		`<link rel="icon" href="/favicon.ico" />`,
		`<link rel="apple-touch-icon" href="/favicon.ico" />`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderApplicationIdentity() = %q, want fragment %q", got, want)
		}
	}
	for _, stale := range []string{"Theme application", "/theme-icon.png", "/theme-touch-icon.png"} {
		if strings.Contains(got, stale) {
			t.Fatalf("renderApplicationIdentity() retained stale metadata %q: %q", stale, got)
		}
	}
	if strings.Count(got, `<link rel="icon" href="/favicon.ico" />`) != 1 {
		t.Fatalf("renderApplicationIdentity() did not normalize favicon declarations: %q", got)
	}
	if strings.Contains(got, "relative-favicon.ico") {
		t.Fatalf("renderApplicationIdentity() retained a route-relative favicon: %q", got)
	}
}

func TestRenderSystemApplicationIdentityLeavesRuntimeTitleOwnershipToReact(t *testing.T) {
	got := renderSystemApplicationIdentity(
		`<html><head><title>Lite</title><link rel="shortcut icon" href="favicon.ico" /></head><body></body></html>`,
		"My Lite",
	)
	if !strings.Contains(got, `<title>My Lite</title>`) {
		t.Fatalf("system document did not receive its initial title: %q", got)
	}
	if strings.Contains(got, documentTitleSyncMarker) || strings.Contains(got, "MutationObserver") {
		t.Fatalf("system document retained the public title synchronizer: %q", got)
	}
	if !strings.Contains(got, `<link rel="icon" href="/favicon.ico" />`) || strings.Contains(got, `href="favicon.ico"`) {
		t.Fatalf("system document did not receive a route-safe favicon: %q", got)
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
		config.SitenameKey:   "My Lite",
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
		expectedTitle := "My Lite"
		if isAdminApplicationPath(tt.path) {
			expectedTitle = adminApplicationTitle
		}
		for _, want := range []string{
			`<title>` + expectedTitle + `</title>`,
			`<meta name="apple-mobile-web-app-title" content="` + expectedTitle + `" />`,
			`<meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover" />`,
			`<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />`,
			`<link rel="icon" href="/favicon.ico" />`,
			`<link rel="apple-touch-icon" href="/favicon.ico" />`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("GET %s body does not contain %q", tt.path, want)
			}
		}
		if !isPrivateApplicationPath(tt.path) {
			if !strings.Contains(body, documentTitleSyncMarker) {
				t.Fatalf("GET %s public document has no title synchronizer", tt.path)
			}
		} else if strings.Contains(body, documentTitleSyncMarker) {
			t.Fatalf("GET %s private system document contains the public title synchronizer", tt.path)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
			t.Fatalf("GET %s Cache-Control = %q", tt.path, got)
		}
	}
}

func TestAdminApplicationPath(t *testing.T) {
	tests := map[string]bool{
		"/admin":         true,
		"/admin/servers": true,
		"/administrator": false,
		"/terminal":      false,
		"/install":       false,
		"/manage":        false,
	}
	for requestPath, want := range tests {
		if got := isAdminApplicationPath(requestPath); got != want {
			t.Fatalf("isAdminApplicationPath(%q) = %t, want %t", requestPath, got, want)
		}
	}
}

func TestStaticServesOneDynamicManifestForPublicAndSystemUI(t *testing.T) {
	t.Chdir(t.TempDir())
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	if err := config.SetMany(map[string]any{
		config.SitenameKey:    "My Lite",
		config.DescriptionKey: "My monitor",
	}); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	Static(router.Group("/"), router.NoRoute)

	for _, requestPath := range []string{"/manifest.json", "/system-assets/manifest.json"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", requestPath, recorder.Code, http.StatusOK)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("GET %s Content-Type = %q", requestPath, got)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
			t.Fatalf("GET %s Cache-Control = %q", requestPath, got)
		}

		var manifest webAppManifest
		if err := json.Unmarshal(recorder.Body.Bytes(), &manifest); err != nil {
			t.Fatalf("decode GET %s: %v", requestPath, err)
		}
		if manifest.ID != "/" || manifest.Name != "My Lite" || manifest.ShortName != "My Lite" {
			t.Fatalf("GET %s identity = (%q, %q, %q)", requestPath, manifest.ID, manifest.Name, manifest.ShortName)
		}
		if manifest.Description != "My monitor" || manifest.StartURL != "/" || manifest.Scope != "/" {
			t.Fatalf("GET %s routing metadata = %#v", requestPath, manifest)
		}
		if len(manifest.Icons) != 1 || manifest.Icons[0] != (webAppManifestIcon{
			Src:     "/favicon.ico",
			Sizes:   "any",
			Type:    "image/x-icon",
			Purpose: "any",
		}) {
			t.Fatalf("GET %s icons = %#v", requestPath, manifest.Icons)
		}
	}
}

func TestEnsureBundledThemesUsesLiteThemeForNewInstall(t *testing.T) {
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
		t.Fatal("bundled Lite Theme Default was not installed")
	}
}

func TestEnsureBundledThemesMigratesLegacyDefaultToLiteTheme(t *testing.T) {
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
		t.Fatal("legacy migration did not install bundled Lite Theme Default")
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

func TestEnsureBundledThemesRefreshesExistingLiteThemeOnce(t *testing.T) {
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
		t.Fatal("existing Lite-Theme was not refreshed")
	}
	if _, err := os.Stat(filepath.Join(DataDir, ThemesDir, DefaultTheme, "Lite-theme.json")); err != nil {
		t.Fatalf("refreshed Lite-Theme missing Lite-theme.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(DataDir, ThemesDir, DefaultTheme, "komari-theme.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy komari-theme.json should have been replaced, err=%v", err)
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

func TestThemeVersionNewer(t *testing.T) {
	cases := []struct {
		candidate, installed string
		want                 bool
	}{
		{"1.0.1", "1.0.0", true},
		{"1.0.0", "1.0.1", false},
		{"1.0.1", "1.0.1", false},
		{"v1.0.1", "1.0.0", true},
		{"1.0.1", "", true},
		{"99.0.0", "1.0.1", true},
	}
	for _, tc := range cases {
		if got := themeVersionNewer(tc.candidate, tc.installed); got != tc.want {
			t.Fatalf("themeVersionNewer(%q, %q) = %v, want %v", tc.candidate, tc.installed, got, tc.want)
		}
	}
}

func writeInstalledLiteTheme(t *testing.T, version, index string) {
	t.Helper()
	dir := filepath.Join(DataDir, ThemesDir, DefaultTheme, DistDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"Lite-Theme","short":"lite-theme","version":"` + version + `"}`
	if err := os.WriteFile(filepath.Join(DataDir, ThemesDir, DefaultTheme, "Lite-theme.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, IndexFile), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureBundledThemesRefreshesWhenBundledVersionIsNewer(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)

	writeInstalledLiteTheme(t, "0.0.1", "stale-older-theme")
	if err := config.SetMany(map[string]any{
		config.ThemeKey:         DefaultTheme,
		themeBundleMigrationKey: currentThemeBundleMigration,
	}); err != nil {
		t.Fatal(err)
	}

	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(DataDir, ThemesDir, DefaultTheme, DistDir, IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(index) == "stale-older-theme" {
		t.Fatal("older installed Lite-Theme was not refreshed from the bundled version")
	}
	if got := localThemeVersion(DefaultTheme); got != embeddedThemeVersion("bundledThemes/Lite-theme") {
		t.Fatalf("installed version = %q, bundled = %q", got, embeddedThemeVersion("bundledThemes/Lite-theme"))
	}
}

func TestEnsureBundledThemesKeepsNewerInstalledLiteTheme(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)

	writeInstalledLiteTheme(t, "99.0.0", "market-newer-theme")
	if err := config.SetMany(map[string]any{
		config.ThemeKey:         DefaultTheme,
		themeBundleMigrationKey: currentThemeBundleMigration,
	}); err != nil {
		t.Fatal(err)
	}

	if err := EnsureBundledThemes(); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(DataDir, ThemesDir, DefaultTheme, DistDir, IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(index) != "market-newer-theme" {
		t.Fatal("newer installed Lite-Theme was overwritten by an older bundled version")
	}
	if got := localThemeVersion(DefaultTheme); got != "99.0.0" {
		t.Fatalf("installed version = %q, want 99.0.0", got)
	}
}

func TestEnsureBundledThemesDoesNotReinstallDeletedLiteTheme(t *testing.T) {
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
		t.Fatal("deleted Lite-Theme was reinstalled for a third-party active theme")
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
	if !strings.Contains(publicPage.Body.String(), "vite-ui-theme") {
		t.Fatal("missing public theme did not use the embedded Lite-Theme rescue page")
	}

	if _, err := fs.ReadFile(PublicFS, "systemUI/dist/index.html"); err != nil {
		t.Skip("system UI dist is injected by CI from Lite-web")
	}

	adminPage := request("/admin/settings/theme")
	if adminPage.Code != http.StatusOK {
		t.Fatalf("system UI status=%d", adminPage.Code)
	}
	if !strings.Contains(adminPage.Body.String(), "/system-assets/") {
		t.Fatal("system UI did not reference its independent asset prefix")
	}
	if strings.Contains(adminPage.Body.String(), "data-public-custom-head") || strings.Contains(adminPage.Body.String(), "vite-ui-theme") {
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
	favicon := request("/favicon.ico")
	if favicon.Code != http.StatusOK {
		t.Fatalf("system favicon fallback status=%d", favicon.Code)
	}
	if ct := favicon.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Fatalf("system favicon fallback content-type=%q", ct)
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

func TestRescueThemeShipsLiteThemeManifest(t *testing.T) {
	if _, err := fs.ReadFile(PublicFS, "rescueTheme/Lite-theme.json"); err != nil {
		t.Fatalf("rescue Lite-theme.json: %v", err)
	}
	if _, err := fs.ReadFile(PublicFS, "rescueTheme/preview.png"); err != nil {
		t.Fatalf("rescue preview.png: %v", err)
	}
	if _, err := fs.ReadFile(PublicFS, "bundledThemes/Lite-theme/preview.png"); err != nil {
		t.Fatalf("bundled preview.png: %v", err)
	}
	if _, err := fs.ReadFile(PublicFS, "rescueTheme/komari-theme.json"); err == nil {
		t.Fatal("rescue theme still ships komari-theme.json")
	}
	index, err := fs.ReadFile(PublicFS, "rescueTheme/dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "/favicon.png") {
		t.Fatal("rescue index is not Lite-Theme")
	}
	if !strings.Contains(string(index), `/assets/index.`) || !strings.Contains(string(index), ".js") {
		t.Fatal("rescue index is not a Lite-Theme production build")
	}
}

func TestIsLocalThemeUsableAcceptsLiteOrLegacyManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	write := func(short, manifestName string) {
		base := filepath.Join(DataDir, ThemesDir, short)
		if err := os.MkdirAll(filepath.Join(base, DistDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, manifestName), []byte(`{"short":"`+short+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, DistDir, IndexFile), []byte("ok"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lite-primary", "Lite-theme.json")
	write("komari-legacy", "komari-theme.json")
	both := filepath.Join(DataDir, ThemesDir, "both")
	if err := os.MkdirAll(filepath.Join(both, DistDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(both, "Lite-theme.json"), []byte(`{"short":"both"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(both, "komari-theme.json"), []byte(`{"short":"both"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(both, DistDir, IndexFile), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, short := range []string{"lite-primary", "komari-legacy", "both"} {
		if !IsLocalThemeUsable(short) {
			t.Fatalf("IsLocalThemeUsable(%q) = false", short)
		}
	}
}

func TestActiveThemeNavigationPrefersLiteManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	themeDir := filepath.Join(DataDir, ThemesDir, "third-party")
	if err := os.MkdirAll(themeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "komari-theme.json"), []byte(`{"navigation":{"server_detail":"/komari/{uuid}"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "Lite-theme.json"), []byte(`{"navigation":{"server_detail":"/lite/{uuid}"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(config.ThemeKey, "third-party"); err != nil {
		t.Fatal(err)
	}
	if got := ActiveThemeNavigation().ServerDetailURL("node-a", 0); got != "/lite/node-a" {
		t.Fatalf("preferred navigation URL = %q", got)
	}
}
