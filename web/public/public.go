package public

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/thememanifest"
)

var legacyDefaultFaviconSHA256 = [32]byte{
	0xbd, 0x28, 0xc1, 0xec, 0x58, 0x09, 0x26, 0xbc,
	0x3f, 0x5e, 0x9c, 0xb2, 0x72, 0x19, 0x76, 0xd9,
	0xeb, 0xad, 0xee, 0xfe, 0x22, 0x1b, 0xae, 0x77,
	0x29, 0x4b, 0xf3, 0x38, 0x85, 0xc7, 0x1a, 0x69,
}

//go:embed systemUI rescueTheme bundledThemes
var PublicFS embed.FS

// 常量定义
const (
	DataDir            = "./data"
	ThemesDir          = "theme"
	FaviconFile        = "favicon.ico"
	AppleTouchIconFile = "apple-touch-icon.png"
	PwaIcon192File     = "android-chrome-192x192.png"
	PwaIcon512File     = "android-chrome-512x512.png"
	DefaultTheme       = "lite-theme"
	LegacyPublicTheme  = "nezha"
	LegacyDefaultTheme = "default"
	LegacyLiteTheme    = "lite-theme-default"
	LanguageCookieName = "language"

	// 主题内部结构定义
	DistDir   = "dist"       // 静态资源存放目录
	IndexFile = "index.html" // 相对于 DistDir
)

const themeBundleMigrationKey = "theme_bundle_migration_v1"

const currentThemeBundleMigration = 10

const adminApplicationTitle = "Lite"

const themeChangeReloadScript = `<script>(()=>{window.addEventListener("storage",(event)=>{if(event.key==="lite-active-theme-changed"||event.key==="komari-active-theme-changed"){window.location.reload();}});})();</script>`

const documentTitleSyncMarker = "data-lite-title-sync"

type webAppManifest struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	ShortName       string               `json:"short_name"`
	Description     string               `json:"description"`
	StartURL        string               `json:"start_url"`
	Scope           string               `json:"scope"`
	Display         string               `json:"display"`
	BackgroundColor string               `json:"background_color"`
	ThemeColor      string               `json:"theme_color"`
	Orientation     string               `json:"orientation"`
	Icons           []webAppManifestIcon `json:"icons"`
}

type webAppManifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

var (
	documentTitlePattern         = regexp.MustCompile(`(?is)<title(?:\s[^>]*)?>.*?</title\s*>`)
	appleApplicationTitlePattern = regexp.MustCompile(`(?is)<meta\s+[^>]*name\s*=\s*["']apple-mobile-web-app-title["'][^>]*>`)
	applicationIconPattern       = regexp.MustCompile(`(?is)<link\s+[^>]*rel\s*=\s*["'](?:shortcut\s+)?icon["'][^>]*>`)
	appleTouchIconPattern        = regexp.MustCompile(`(?is)<link\s+[^>]*rel\s*=\s*["']apple-touch-icon["'][^>]*>`)
	viewportMetaPattern          = regexp.MustCompile(`(?is)<meta\s+[^>]*name\s*=\s*["']viewport["'][^>]*>`)
	appleStatusBarPattern        = regexp.MustCompile(`(?is)<meta\s+[^>]*name\s*=\s*["']apple-mobile-web-app-status-bar-style["'][^>]*>`)
	headClosePattern             = regexp.MustCompile(`(?i)</head\s*>`)
	bodyClosePattern             = regexp.MustCompile(`(?i)</body\s*>`)
	themeVersionPattern          = regexp.MustCompile(`^(\d+)(?:\.(\d+))?(?:\.(\d+))?`)
)

func injectThemeChangeReload(html string) string {
	if strings.Contains(html, themeChangeReloadScript) {
		return html
	}
	if strings.Contains(html, "</body>") {
		return strings.Replace(html, "</body>", themeChangeReloadScript+"</body>", 1)
	}
	return html + themeChangeReloadScript
}

func injectCustomHTML(htmlStr, customHead, customBody string) string {
	if location := bodyClosePattern.FindStringIndex(htmlStr); location != nil {
		htmlStr = htmlStr[:location[0]] + customBody + htmlStr[location[0]:]
	} else {
		htmlStr += customBody
	}
	if location := headClosePattern.FindStringIndex(htmlStr); location != nil {
		return htmlStr[:location[0]] + customHead + htmlStr[location[0]:]
	}
	return customHead + htmlStr
}

func renderDocumentTitle(htmlStr, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Lite"
	}

	titleTag := "<title>" + html.EscapeString(title) + "</title>"
	if location := documentTitlePattern.FindStringIndex(htmlStr); location != nil {
		htmlStr = htmlStr[:location[0]] + titleTag + htmlStr[location[1]:]
	} else if location := headClosePattern.FindStringIndex(htmlStr); location != nil {
		htmlStr = htmlStr[:location[0]] + titleTag + htmlStr[location[0]:]
	} else {
		htmlStr = titleTag + htmlStr
	}

	return htmlStr
}

func renderPublicDocumentTitle(htmlStr, title string) string {
	htmlStr = renderDocumentTitle(htmlStr, title)
	if strings.Contains(htmlStr, documentTitleSyncMarker) {
		return htmlStr
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = "Lite"
	}
	encodedTitle, _ := json.Marshal(title)
	script := `<script ` + documentTitleSyncMarker + `>(()=>{const expectedTitle=` + string(encodedTitle) + `;const syncTitle=()=>{if(document.title!==expectedTitle){document.title=expectedTitle;}};syncTitle();if(document.head){new MutationObserver(syncTitle).observe(document.head,{childList:true,subtree:true,characterData:true});}})();</script>`
	if location := bodyClosePattern.FindStringIndex(htmlStr); location != nil {
		return htmlStr[:location[0]] + script + htmlStr[location[0]:]
	}
	return htmlStr + script
}

const (
	mobileViewportTag = `<meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover" />`
	appleStatusBarTag = `<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent" />`
)

func replaceOrInsertHeadTag(htmlStr, tag string, pattern *regexp.Regexp) string {
	if pattern.MatchString(htmlStr) {
		return pattern.ReplaceAllString(htmlStr, tag)
	}
	if location := headClosePattern.FindStringIndex(htmlStr); location != nil {
		return htmlStr[:location[0]] + tag + htmlStr[location[0]:]
	}
	return tag + htmlStr
}

func renderMobileChromeMeta(htmlStr string) string {
	htmlStr = replaceOrInsertHeadTag(htmlStr, mobileViewportTag, viewportMetaPattern)
	return replaceOrInsertHeadTag(htmlStr, appleStatusBarTag, appleStatusBarPattern)
}

func renderApplicationIdentityWithTitle(htmlStr, title string, synchronizeTitle bool) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Lite"
	}

	htmlStr = renderMobileChromeMeta(htmlStr)
	if synchronizeTitle {
		htmlStr = renderPublicDocumentTitle(htmlStr, title)
	} else {
		htmlStr = renderDocumentTitle(htmlStr, title)
	}
	appleTitle := `<meta name="apple-mobile-web-app-title" content="` + html.EscapeString(title) + `" />`
	if appleApplicationTitlePattern.MatchString(htmlStr) {
		htmlStr = appleApplicationTitlePattern.ReplaceAllString(htmlStr, appleTitle)
	} else if location := headClosePattern.FindStringIndex(htmlStr); location != nil {
		htmlStr = htmlStr[:location[0]] + appleTitle + htmlStr[location[0]:]
	}

	// Remove every theme-provided icon declaration before adding the canonical
	// root paths. This also covers legacy `rel="shortcut icon"` declarations,
	// which otherwise resolve below deep SPA routes such as /admin/settings.
	htmlStr = applicationIconPattern.ReplaceAllString(htmlStr, "")
	htmlStr = appleTouchIconPattern.ReplaceAllString(htmlStr, "")
	if location := headClosePattern.FindStringIndex(htmlStr); location != nil {
		htmlStr = htmlStr[:location[0]] + applicationIconHTML + htmlStr[location[0]:]
	}
	return htmlStr
}

func renderApplicationIdentity(htmlStr, title string) string {
	return renderApplicationIdentityWithTitle(htmlStr, title, true)
}

func renderSystemApplicationIdentity(htmlStr, title string) string {
	return renderApplicationIdentityWithTitle(htmlStr, title, false)
}

func renderWebAppManifest(siteName, description string) webAppManifest {
	siteName = strings.TrimSpace(siteName)
	if siteName == "" {
		siteName = "Lite"
	}
	description = strings.TrimSpace(description)
	if description == "" {
		description = config.DefaultSiteDescription
	}

	return webAppManifest{
		ID:              "/",
		Name:            siteName,
		ShortName:       siteName,
		Description:     description,
		StartURL:        "/",
		Scope:           "/",
		Display:         "standalone",
		BackgroundColor: "#ffffff",
		ThemeColor:      "#2563eb",
		Orientation:     "portrait-primary",
		Icons: []webAppManifestIcon{
			{
				Src:     "/apple-touch-icon.png",
				Sizes:   "180x180",
				Type:    "image/png",
				Purpose: "any",
			},
			{
				Src:     "/android-chrome-192x192.png",
				Sizes:   "192x192",
				Type:    "image/png",
				Purpose: "any",
			},
			{
				Src:     "/android-chrome-512x512.png",
				Sizes:   "512x512",
				Type:    "image/png",
				Purpose: "any",
			},
		},
	}
}

func injectSiteDescription(html, description string) string {
	for _, placeholder := range []string{
		config.DefaultSiteDescription,
		"A simple server monitor tool.",
	} {
		html = strings.ReplaceAll(html, placeholder, description)
	}
	return html
}

func init() {
	_ = os.MkdirAll("./data/theme", 0755)
	if _, err := removeFaviconIfHashMatches(
		filepath.Join(DataDir, FaviconFile),
		legacyDefaultFaviconSHA256,
	); err != nil {
		log.Printf("Failed to migrate legacy default favicon: %v", err)
	}
}

func removeFaviconIfHashMatches(filePath string, expectedHash [32]byte) (bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	if sha256.Sum256(data) != expectedHash {
		return false, nil
	}
	if err := os.Remove(filePath); err != nil {
		return false, err
	}
	return true, nil
}

func normalizeHTMLLanguage(language string) string {
	language = strings.TrimSpace(strings.ReplaceAll(language, "_", "-"))
	if len(language) < 2 || len(language) > 32 {
		return ""
	}

	for _, r := range language {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return ""
	}

	return language
}

func replaceHTMLLanguage(htmlStr, language string) string {
	language = normalizeHTMLLanguage(language)
	if language == "" {
		return htmlStr
	}

	replacements := []struct {
		old string
		new string
	}{
		{`<html lang="en">`, `<html lang="` + language + `">`},
		{`<html lang='en'>`, `<html lang='` + language + `'>`},
		{`<html>`, `<html lang="` + language + `">`},
	}

	for _, replacement := range replacements {
		if strings.Contains(htmlStr, replacement.old) {
			return strings.Replace(htmlStr, replacement.old, replacement.new, 1)
		}
	}

	return htmlStr
}

// isSafePath 验证路径是否在指定的基础目录内，防止路径穿透攻击
func isSafePath(basePath, targetPath string) bool {
	// 获取基础目录的绝对路径
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return false
	}

	// 清理目标路径，移除 ../ 等
	cleanTarget := filepath.Clean(targetPath)

	// 拼接完整路径
	fullPath := filepath.Join(absBase, cleanTarget)

	// 获取绝对路径
	absTarget, err := filepath.Abs(fullPath)
	if err != nil {
		return false
	}

	// 检查目标路径是否以基础路径开头
	// 使用 filepath.Rel 更可靠地检查路径关系
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}

	// 如果相对路径以 .. 开头，说明目标在基础目录之外
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

func embeddedFileContent(root, relativePath string) ([]byte, string, bool) {
	cleanPath := path.Clean(strings.TrimPrefix(filepath.ToSlash(relativePath), "/"))
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return nil, "", false
	}
	content, err := fs.ReadFile(PublicFS, path.Join(root, cleanPath))
	if err != nil {
		return nil, "", false
	}
	return content, contentTypeForPath(cleanPath), true
}

func contentTypeForPath(filePath string) string {
	if strings.EqualFold(filepath.Ext(filePath), ".ico") {
		return "image/x-icon"
	}
	return mime.TypeByExtension(filepath.Ext(filePath))
}

func detectImageContentType(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return "image/x-icon"
	}
}

func readUploadedFavicon() ([]byte, string, bool) {
	data, err := os.ReadFile(filepath.Join(DataDir, FaviconFile))
	if err != nil || len(data) == 0 {
		return nil, "", false
	}
	return data, detectImageContentType(data), true
}

func validThemeID(themeID string) bool {
	return themeID != "" && !strings.Contains(themeID, "..") &&
		!strings.ContainsAny(themeID, `/\\`)
}

func localThemeFileContent(themeID, relativePath string) ([]byte, string, bool) {
	if !validThemeID(themeID) {
		return nil, "", false
	}
	cleanPath := filepath.Clean(strings.TrimPrefix(relativePath, "/"))
	themeBasePath := filepath.Join(DataDir, ThemesDir, themeID)
	if !isSafePath(themeBasePath, cleanPath) {
		return nil, "", false
	}
	localPath := filepath.Join(themeBasePath, cleanPath)
	if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
		content, err := os.ReadFile(localPath)
		if err == nil {
			return content, contentTypeForPath(localPath), true
		}
	}
	return nil, "", false
}

func IsLocalThemeUsable(themeID string) bool {
	if !validThemeID(themeID) {
		return false
	}
	base := filepath.Join(DataDir, ThemesDir, themeID)
	if _, ok := thememanifest.FindInDir(base); !ok {
		return false
	}
	info, err := os.Stat(filepath.Join(base, DistDir, IndexFile))
	return err == nil && !info.IsDir()
}

func installEmbeddedTheme(root, themeID string) error {
	return installEmbeddedThemeWithReplace(root, themeID, false)
}

func installEmbeddedThemeWithReplace(root, themeID string, replace bool) error {
	if !replace && IsLocalThemeUsable(themeID) {
		return nil
	}
	themesDir := filepath.Join(DataDir, ThemesDir)
	if err := os.MkdirAll(themesDir, 0o755); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(themesDir, "."+themeID+"-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	if err := fs.WalkDir(PublicFS, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative := strings.TrimPrefix(name, root+"/")
		if relative == name || relative == "" {
			return nil
		}
		target := filepath.Join(stageDir, filepath.FromSlash(relative))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := fs.ReadFile(PublicFS, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	}); err != nil {
		return err
	}

	finalDir := filepath.Join(themesDir, themeID)
	backupDir := finalDir + ".previous"
	_ = os.RemoveAll(backupDir)
	if _, err := os.Stat(finalDir); err == nil {
		if err := os.Rename(finalDir, backupDir); err != nil {
			return err
		}
	}
	if err := os.Rename(stageDir, finalDir); err != nil {
		_ = os.Rename(backupDir, finalDir)
		return err
	}
	_ = os.RemoveAll(backupDir)
	return nil
}

func themeManifestVersion(content []byte) string {
	var meta struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(content, &meta) != nil {
		return ""
	}
	return strings.TrimSpace(meta.Version)
}

func parseThemeVersion(value string) ([3]int, bool) {
	value = strings.TrimSpace(value)
	if value != "" && (value[0] == 'v' || value[0] == 'V') {
		value = value[1:]
	}
	match := themeVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return [3]int{}, false
	}
	part := func(index int) int {
		if index >= len(match) || match[index] == "" {
			return 0
		}
		n := 0
		fmt.Sscanf(match[index], "%d", &n)
		return n
	}
	return [3]int{part(1), part(2), part(3)}, true
}

func themeVersionNewer(candidate, installed string) bool {
	next, nextOK := parseThemeVersion(candidate)
	current, currentOK := parseThemeVersion(installed)
	if !nextOK || !currentOK {
		return candidate != installed
	}
	for i := range next {
		if next[i] != current[i] {
			return next[i] > current[i]
		}
	}
	return false
}

func localThemeVersion(themeID string) string {
	manifestPath, ok := thememanifest.FindInDir(filepath.Join(DataDir, ThemesDir, themeID))
	if !ok {
		return ""
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		return ""
	}
	return themeManifestVersion(content)
}

func embeddedThemeVersion(root string) string {
	for _, name := range thememanifest.Names() {
		content, err := fs.ReadFile(PublicFS, path.Join(root, name))
		if err != nil {
			continue
		}
		if version := themeManifestVersion(content); version != "" {
			return version
		}
	}
	return ""
}

func refreshBundledLiteThemeIfNewer() error {
	if !IsLocalThemeUsable(DefaultTheme) {
		return nil
	}
	embedded := embeddedThemeVersion("bundledThemes/Lite-theme")
	installed := localThemeVersion(DefaultTheme)
	if embedded == "" || !themeVersionNewer(embedded, installed) {
		return nil
	}
	if err := installEmbeddedThemeWithReplace("bundledThemes/Lite-theme", DefaultTheme, true); err != nil {
		return fmt.Errorf("refresh bundled Lite-Theme: %w", err)
	}
	return nil
}

func localThemeFallback() string {
	if IsLocalThemeUsable(DefaultTheme) {
		return DefaultTheme
	}
	entries, err := os.ReadDir(filepath.Join(DataDir, ThemesDir))
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && IsLocalThemeUsable(entry.Name()) {
			return entry.Name()
		}
	}
	return ""
}

// EnsureBundledThemes performs the one-time transition from the former
// inseparable default frontend to managed public themes.
func EnsureBundledThemes() error {
	migrated, err := config.GetAs[int](themeBundleMigrationKey, 0)
	if err != nil {
		return err
	}
	currentTheme, err := config.GetAs[string](config.ThemeKey, DefaultTheme)
	if err != nil {
		return err
	}
	if currentTheme == "" {
		currentTheme = DefaultTheme
	}
	if currentTheme == LegacyDefaultTheme || currentTheme == LegacyLiteTheme {
		currentTheme = DefaultTheme
	}
	if migrated < 1 {
		if err := installEmbeddedTheme("bundledThemes/Lite-theme", DefaultTheme); err != nil {
			return fmt.Errorf("install bundled Lite-Theme: %w", err)
		}
		currentTheme = DefaultTheme
	}
	// Migrate the former built-in public themes to Lite-Theme once. A user who
	// selected an unrelated custom theme keeps that choice.
	if migrated >= 1 && migrated < currentThemeBundleMigration {
		switchToLiteDefault := currentTheme == LegacyPublicTheme || currentTheme == LegacyDefaultTheme || currentTheme == LegacyLiteTheme || currentTheme == DefaultTheme
		if switchToLiteDefault || IsLocalThemeUsable(DefaultTheme) {
			if err := installEmbeddedThemeWithReplace("bundledThemes/Lite-theme", DefaultTheme, true); err != nil {
				return fmt.Errorf("install bundled Lite-Theme: %w", err)
			}
		}
		if currentTheme == LegacyPublicTheme || currentTheme == LegacyDefaultTheme || currentTheme == LegacyLiteTheme {
			currentTheme = DefaultTheme
		}
	}
	if err := refreshBundledLiteThemeIfNewer(); err != nil {
		return err
	}
	if !IsLocalThemeUsable(currentTheme) {
		currentTheme = localThemeFallback()
		if currentTheme == "" {
			if err := installEmbeddedTheme("bundledThemes/Lite-theme", DefaultTheme); err != nil {
				return fmt.Errorf("restore bundled Lite-Theme: %w", err)
			}
			currentTheme = DefaultTheme
		}
	}
	return config.SetMany(map[string]any{
		config.ThemeKey:         currentTheme,
		themeBundleMigrationKey: currentThemeBundleMigration,
	})
}

// Static 注册静态资源和 SPA 路由处理
func Static(r *gin.RouterGroup, noRoute func(handlers ...gin.HandlerFunc)) {
	getConfig := func() map[string]any {
		cfg, _ := config.GetMany(map[string]any{
			config.DescriptionKey: config.DefaultSiteDescription,
			config.CustomHeadKey:  "",
			config.CustomBodyKey:  "",
			config.SitenameKey:    "Lite",
			config.ThemeKey:       DefaultTheme,
		})
		return cfg
	}

	getPublicFileContent := func(themeID, relativePath string) ([]byte, string, bool) {
		if IsLocalThemeUsable(themeID) {
			return localThemeFileContent(themeID, relativePath)
		}
		return embeddedFileContent("rescueTheme", relativePath)
	}

	serveWebAppManifest := func(c *gin.Context) {
		cfg := getConfig()
		setNoStoreHeaders(c)
		c.JSON(http.StatusOK, renderWebAppManifest(
			cfg[config.SitenameKey].(string),
			cfg[config.DescriptionKey].(string),
		))
	}

	// 核心逻辑：渲染 Index.html
	serveIndex := func(c *gin.Context) {
		reqPath := c.Request.URL.Path
		// index.html contains live site metadata and must never be served stale.
		setNoStoreHeaders(c)
		cfg := getConfig()

		privateApplication := isPrivateApplicationPath(reqPath)
		targetFile := path.Join(DistDir, IndexFile)
		var content []byte
		var exists bool
		if privateApplication {
			content, _, exists = embeddedFileContent("systemUI", targetFile)
		} else {
			content, _, exists = getPublicFileContent(cfg[config.ThemeKey].(string), targetFile)
		}

		if !exists {
			c.String(http.StatusNotFound, "Application index is unavailable.")
			return
		}

		htmlStr := string(content)
		if language, err := c.Cookie(LanguageCookieName); err == nil {
			htmlStr = replaceHTMLLanguage(htmlStr, language)
		}
		if privateApplication {
			// React owns route-specific titles after the system UI starts. Keeping
			// the public-theme title observer out of private applications prevents
			// it from fighting the admin title on every DOM mutation.
			title := cfg[config.SitenameKey].(string)
			if isAdminApplicationPath(reqPath) {
				title = adminApplicationTitle
			}
			htmlStr = renderSystemApplicationIdentity(htmlStr, title)
		} else {
			htmlStr = renderApplicationIdentity(htmlStr, cfg[config.SitenameKey].(string))
		}

		// Custom Head/Body content belongs to the public site only. Keeping the
		// private applications on the built-in document prevents public CSS and
		// scripts from changing the admin or terminal interfaces.
		if privateApplication {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(htmlStr))
			return
		}

		htmlStr = injectCustomHTML(
			htmlStr,
			cfg[config.CustomHeadKey].(string),
			cfg[config.CustomBodyKey].(string),
		)

		rendered := injectSiteDescription(
			htmlStr,
			cfg[config.DescriptionKey].(string),
		)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(injectThemeChangeReload(rendered)))
	}

	// ================= 路由定义 =================

	writeIcon := func(c *gin.Context, data []byte, mimeType string, size int, flatten bool) {
		if flatten {
			if rendered, ok := opaquePwaIconPNG(data, size); ok {
				c.Data(http.StatusOK, "image/png", rendered)
				return
			}
		}
		c.Data(http.StatusOK, mimeType, data)
	}

	serveNamedIcon := func(c *gin.Context, size int, flatten bool, names ...string) {
		cfg := getConfig()
		themeID := cfg[config.ThemeKey].(string)
		for _, name := range names {
			content, mimeType, exists := getPublicFileContent(themeID, path.Join(DistDir, name))
			if exists {
				writeIcon(c, content, mimeType, size, flatten)
				return
			}
		}
		for _, name := range names {
			content, mimeType, exists := embeddedFileContent("systemUI", path.Join(DistDir, name))
			if exists {
				writeIcon(c, content, mimeType, size, flatten)
				return
			}
		}
		for _, fallback := range []string{
			path.Join(DistDir, "favicon.png"),
			path.Join(DistDir, "assets/pwa-icon.png"),
		} {
			content, mimeType, exists := embeddedFileContent("systemUI", fallback)
			if exists {
				writeIcon(c, content, mimeType, size, flatten)
				return
			}
		}
		c.Status(http.StatusNotFound)
	}

	serveTabIcon := func(c *gin.Context, size int, names ...string) {
		setNoStoreHeaders(c)
		if data, mimeType, ok := readUploadedFavicon(); ok {
			writeIcon(c, data, mimeType, size, false)
			return
		}
		serveNamedIcon(c, size, false, names...)
	}

	servePwaIcon := func(c *gin.Context, size int, names ...string) {
		setNoStoreHeaders(c)
		if data, mimeType, ok := readUploadedFavicon(); ok {
			writeIcon(c, data, mimeType, size, true)
			return
		}
		serveNamedIcon(c, size, false, names...)
	}

	r.GET("/favicon.ico", func(c *gin.Context) {
		serveTabIcon(c, pwaIcon192Size, "favicon.png", FaviconFile)
	})

	r.GET("/apple-touch-icon.png", func(c *gin.Context) {
		servePwaIcon(c, defaultPwaIconSize, AppleTouchIconFile, PwaIcon192File, PwaIcon512File, "favicon.png")
	})
	r.GET("/android-chrome-192x192.png", func(c *gin.Context) {
		servePwaIcon(c, pwaIcon192Size, PwaIcon192File, PwaIcon512File, AppleTouchIconFile, "favicon.png")
	})
	r.GET("/android-chrome-512x512.png", func(c *gin.Context) {
		servePwaIcon(c, pwaIcon512Size, PwaIcon512File, PwaIcon192File, AppleTouchIconFile, "favicon.png")
	})
	r.GET("/favicon.png", func(c *gin.Context) {
		serveTabIcon(c, pwaIcon192Size, "favicon.png", FaviconFile)
	})

	r.GET("/manifest.json", serveWebAppManifest)

	// System application assets are immutable and independent from public themes.
	r.GET("/system-assets/*path", func(c *gin.Context) {
		if c.Param("path") == "/manifest.json" {
			serveWebAppManifest(c)
			return
		}
		filePath := path.Join(DistDir, c.Param("path"))
		content, mimeType, exists := embeddedFileContent("systemUI", filePath)
		if !exists {
			c.Status(http.StatusNotFound)
			return
		}
		setStaticCacheHeaders(c, c.Request.URL.Path)
		c.Data(http.StatusOK, mimeType, content)
	})

	// 2. Static theme files are served only from installed, manageable themes.
	// 允许访问 /themes/MyTheme/Lite-theme.json 和 /themes/MyTheme/dist/assets/a.js
	r.GET("/themes/:id/*path", func(c *gin.Context) {
		themeID := c.Param("id")
		// c.Param("path") 包含了开头的 /，getFileContent 会处理
		filePath := c.Param("path")
		serveThemeFile(c, themeID, filePath)
	})

	// 3. SPA 路由 (noRoute)
	noRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet {
			c.Status(http.StatusNotFound)
			return
		}
		//
		func() {
			tempKey := c.Query("temp_key")
			if tempKey == "" {
				return
			}

			tempKeyExpireTime, err := config.GetAs[int64]("tempory_share_token_expire_at", 0)
			if err != nil {
				return
			}
			allowTempKey, err := config.GetAs[string]("tempory_share_token", "")
			if err != nil {
				return
			}

			if allowTempKey == "" || tempKey != allowTempKey {
				return
			}
			now := time.Now().Unix()
			if tempKeyExpireTime < now {
				return
			}
			expireSeconds := int(tempKeyExpireTime - now)
			if expireSeconds > 0 {
				c.SetCookie(
					"temp_key",    // key
					tempKey,       // value
					expireSeconds, // maxAge（秒）
					"/",           // path
					"",            // domain
					false,         // secure
					false,         // httpOnly
				)
			}
		}()
		reqPath := c.Request.URL.Path
		cfg := getConfig()
		// index.html is a live document, not a static asset. It must pass
		// through serveIndex so site metadata and custom Head/Body content
		// are applied even when a browser requests the file explicitly.
		if reqPath == "/index.html" {
			serveIndex(c)
			return
		}

		distPath := path.Join(DistDir, reqPath)

		var content []byte
		var mimeType string
		var exists bool
		if !isPrivateApplicationPath(reqPath) {
			content, mimeType, exists = getPublicFileContent(cfg[config.ThemeKey].(string), distPath)
		}
		if exists {
			setStaticCacheHeaders(c, reqPath)
			c.Data(http.StatusOK, mimeType, content)
			return
		}

		if ext := filepath.Ext(reqPath); ext != "" && ext != ".html" {
			c.Status(http.StatusNotFound)
			return
		}

		// 路由 (如 /dashboard, /settings) -> 返回 index.html
		serveIndex(c)
	})
}

func setNoStoreHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
}

func isPrivateApplicationPath(requestPath string) bool {
	for _, prefix := range []string{"/admin", "/terminal", "/install", "/manage"} {
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}
	return false
}

func isAdminApplicationPath(requestPath string) bool {
	return requestPath == "/admin" || strings.HasPrefix(requestPath, "/admin/")
}

func setStaticCacheHeaders(c *gin.Context, requestPath string) {
	name := strings.ToLower(path.Base(requestPath))
	switch name {
	case "index.html", "sw.js", "service-worker.js", "registersw.js", "manifest.json", "manifest.webmanifest":
		setNoStoreHeaders(c)
		return
	}
	if (strings.HasPrefix(requestPath, "/assets/") || strings.HasPrefix(requestPath, "/system-assets/assets/")) &&
		strings.Contains(strings.TrimSuffix(name, filepath.Ext(name)), "-") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
}
