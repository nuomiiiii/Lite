package public

import (
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
	"github.com/komari-monitor/komari/pkg/config"
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
	DefaultTheme       = "nezha"
	LegacyDefaultTheme = "default"
	ClassicTheme       = "komari-classic"
	LanguageCookieName = "language"

	// 主题内部结构定义
	DistDir   = "dist"       // 静态资源存放目录
	IndexFile = "index.html" // 相对于 DistDir
)

const themeBundleMigrationKey = "theme_bundle_migration_v1"

const currentThemeBundleMigration = 3

const themeChangeReloadScript = `<script>(()=>{window.addEventListener("storage",(event)=>{if(event.key==="komari-active-theme-changed"){window.location.reload();}});})();</script>`

const documentTitleSyncMarker = "data-komari-title-sync"

var (
	documentTitlePattern = regexp.MustCompile(`(?is)<title(?:\s[^>]*)?>.*?</title\s*>`)
	headClosePattern     = regexp.MustCompile(`(?i)</head\s*>`)
	bodyClosePattern     = regexp.MustCompile(`(?i)</body\s*>`)
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

func renderPublicDocumentTitle(htmlStr, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Komari Lite"
	}

	titleTag := "<title>" + html.EscapeString(title) + "</title>"
	if location := documentTitlePattern.FindStringIndex(htmlStr); location != nil {
		htmlStr = htmlStr[:location[0]] + titleTag + htmlStr[location[1]:]
	} else if location := headClosePattern.FindStringIndex(htmlStr); location != nil {
		htmlStr = htmlStr[:location[0]] + titleTag + htmlStr[location[0]:]
	} else {
		htmlStr = titleTag + htmlStr
	}

	if strings.Contains(htmlStr, documentTitleSyncMarker) {
		return htmlStr
	}

	encodedTitle, _ := json.Marshal(title)
	script := `<script ` + documentTitleSyncMarker + `>(()=>{const expectedTitle=` + string(encodedTitle) + `;const syncTitle=()=>{if(document.title!==expectedTitle){document.title=expectedTitle;}};syncTitle();if(document.head){new MutationObserver(syncTitle).observe(document.head,{childList:true,subtree:true,characterData:true});}})();</script>`
	if location := bodyClosePattern.FindStringIndex(htmlStr); location != nil {
		return htmlStr[:location[0]] + script + htmlStr[location[0]:]
	}
	return htmlStr + script
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
	for _, relativePath := range []string{"komari-theme.json", filepath.Join(DistDir, IndexFile)} {
		info, err := os.Stat(filepath.Join(base, relativePath))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
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

func localThemeFallback() string {
	for _, preferred := range []string{DefaultTheme, ClassicTheme} {
		if IsLocalThemeUsable(preferred) {
			return preferred
		}
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
	if currentTheme == LegacyDefaultTheme {
		currentTheme = DefaultTheme
	}
	if migrated < 1 {
		if err := installEmbeddedTheme("bundledThemes/nezha", DefaultTheme); err != nil {
			return fmt.Errorf("install bundled Nezha theme: %w", err)
		}
	}
	// The first decoupled snapshot installed Nezha as a managed theme but did
	// not refresh that copy on later Komari upgrades. Replace an existing copy
	// once so deployments do not keep an old router/API bundle indefinitely.
	// A user who deleted Nezha and selected another theme keeps that choice.
	if migrated >= 1 && migrated < currentThemeBundleMigration && IsLocalThemeUsable(DefaultTheme) {
		if err := installEmbeddedThemeWithReplace("bundledThemes/nezha", DefaultTheme, true); err != nil {
			return fmt.Errorf("refresh bundled Nezha theme: %w", err)
		}
	}
	if !IsLocalThemeUsable(currentTheme) {
		currentTheme = localThemeFallback()
		if currentTheme == "" {
			if err := installEmbeddedTheme("bundledThemes/nezha", DefaultTheme); err != nil {
				return fmt.Errorf("restore bundled Nezha theme: %w", err)
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
			config.DescriptionKey: "A simple server monitor tool.",
			config.CustomHeadKey:  "",
			config.CustomBodyKey:  "",
			config.SitenameKey:    "Komari Lite",
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

		rendered := renderPublicDocumentTitle(
			strings.ReplaceAll(
				htmlStr,
				"A simple server monitor tool.",
				cfg[config.DescriptionKey].(string),
			),
			cfg[config.SitenameKey].(string),
		)
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(injectThemeChangeReload(rendered)))
	}

	// ================= 路由定义 =================

	// 1. Favicon 优先策略
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")

		// 优先：./data/favicon.ico
		localFavicon := filepath.Join(DataDir, FaviconFile)
		if _, err := os.Stat(localFavicon); err == nil {
			c.Header("Content-Type", contentTypeForPath(localFavicon))
			c.File(localFavicon)
			return
		}

		// 其次：当前主题的 dist/favicon.ico 或 theme_root/favicon.ico ?
		// 通常构建后的资源在 dist 中，这里假设优先找 dist 内的，如果你的 favicon 在根目录，去掉 DistDir 拼接即可
		cfg := getConfig()
		themeFaviconPath := path.Join(DistDir, FaviconFile)
		content, mimeType, exists := getPublicFileContent(cfg[config.ThemeKey].(string), themeFaviconPath)
		if exists {
			c.Data(http.StatusOK, mimeType, content)
			return
		}

		// Fresh installations and themes without their own favicon use the
		// system UI icon instead of returning a broken image.
		content, mimeType, exists = embeddedFileContent("systemUI", themeFaviconPath)
		if exists {
			c.Data(http.StatusOK, mimeType, content)
			return
		}

		c.Status(http.StatusNotFound)
	})

	// System application assets are immutable and independent from public themes.
	r.GET("/system-assets/*path", func(c *gin.Context) {
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
	// 允许访问 /themes/MyTheme/theme.json 和 /themes/MyTheme/dist/assets/a.js
	r.GET("/themes/:id/*path", func(c *gin.Context) {
		themeID := c.Param("id")
		// c.Param("path") 包含了开头的 /，getFileContent 会处理
		filePath := c.Param("path")

		content, mimeType, exists := localThemeFileContent(themeID, filePath)
		if exists {
			c.Data(http.StatusOK, mimeType, content)
			return
		}
		c.Status(http.StatusNotFound)
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

func setStaticCacheHeaders(c *gin.Context, requestPath string) {
	name := strings.ToLower(path.Base(requestPath))
	switch name {
	case "index.html", "sw.js", "service-worker.js", "registersw.js", "manifest.webmanifest":
		setNoStoreHeaders(c)
		return
	}
	if (strings.HasPrefix(requestPath, "/assets/") || strings.HasPrefix(requestPath, "/system-assets/assets/")) &&
		strings.Contains(strings.TrimSuffix(name, filepath.Ext(name)), "-") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
}
