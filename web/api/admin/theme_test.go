package admin

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestIsValidThemeShort_PathTraversal 防止 DeleteTheme/UpdateTheme/SetTheme
// 的路径穿越漏洞：req.Short 直接进入 filepath.Join("./data/theme", short)，
// 若 short 含 "../" 会规范化到 ./data/theme 之外，配合 os.RemoveAll 可删除
// 工作目录外任意目录。isValidThemeShort 必须拒绝所有此类 payload。
func TestIsValidThemeShort_PathTraversal(t *testing.T) {
	// 必须被拒绝：路径穿越 / 绝对路径 / 目录分隔符 / 空值 / default
	deny := []string{
		"",
		"default",
		"..",
		"../",
		"./",
		"../../etc",
		"../..",
		"foo/../bar",
		"/etc/passwd",
		"foo/bar",
		"foo\\bar",
		"a b",
		"a;b",
		"a$(id)",
	}
	for _, in := range deny {
		if isValidThemeShort(in) {
			t.Errorf("isValidThemeShort(%q) = true, want false (路径穿越/非法字符未被拦截)", in)
		}
	}

	// 必须被接受：仅字母数字下划线连字符
	accept := []string{
		"mytheme",
		"my-theme",
		"my_theme",
		"theme123",
		"ABC",
		"a",
	}
	for _, in := range accept {
		if !isValidThemeShort(in) {
			t.Errorf("isValidThemeShort(%q) = false, want true (合法名称被误拒)", in)
		}
	}
}

func themeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func closeThemeTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
	})
}

func writeUsableTheme(t *testing.T, short, manifestName, manifest string) {
	t.Helper()
	themeDir := filepath.Join("data", "theme", short)
	if err := os.MkdirAll(filepath.Join(themeDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, manifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "dist", "index.html"), []byte(short), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledThemesPrefersLiteManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	writeUsableTheme(t, "prefer-lite", "Lite-theme.json", `{"name":"FromLite","short":"prefer-lite","configuration":{"type":"managed","data":[]}}`)
	writeUsableTheme(t, "legacy-only", "komari-theme.json", `{"name":"FromKomari","short":"legacy-only","configuration":{"type":"managed","data":[]}}`)
	if err := os.WriteFile(filepath.Join("data", "theme", "prefer-lite", "komari-theme.json"), []byte(`{"name":"FromKomari","short":"prefer-lite","configuration":{"type":"managed","data":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	themes, err := installedThemes()
	if err != nil {
		t.Fatal(err)
	}
	byShort := map[string]string{}
	for _, theme := range themes {
		name, _ := theme.Name.(string)
		byShort[theme.Short] = name
	}
	if byShort["prefer-lite"] != "FromLite" {
		t.Fatalf("prefer-lite name = %q, want FromLite", byShort["prefer-lite"])
	}
	if byShort["legacy-only"] != "FromKomari" {
		t.Fatalf("legacy-only name = %q, want FromKomari", byShort["legacy-only"])
	}
}

func TestExtractThemePrefersLiteManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	archive := themeArchive(t, map[string]string{
		"Lite-theme.json":   `{"name":"FromLite","short":"prefer-lite","configuration":{"type":"managed","data":[]}}`,
		"komari-theme.json": `{"name":"FromKomari","short":"prefer-lite","configuration":{"type":"managed","data":[]}}`,
		"dist/index.html":   "ok",
	})
	archivePath, err := temporaryThemeArchive(archive, "prefer-lite")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archivePath)
	info, err := extractAndValidateTheme(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := info.Name.(string); name != "FromLite" {
		t.Fatalf("extracted name = %q, want FromLite", name)
	}
}

func TestZipEntryCleanNameAcceptsWindowsSeparators(t *testing.T) {
	got := zipEntryCleanName(`dist\index.html`)
	want := filepath.Join("dist", "index.html")
	if got != want {
		t.Fatalf("zipEntryCleanName(%q) = %q, want %q", `dist\index.html`, got, want)
	}
}

func TestExtractThemeAcceptsWindowsBackslashPaths(t *testing.T) {
	t.Chdir(t.TempDir())
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range map[string]string{
		"Lite-theme.json":    `{"name":"WinZip","short":"win-zip","configuration":{"type":"managed","data":[]}}`,
		`dist\index.html`:    "ok",
		`dist\assets\app.js`: "js",
	} {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath, err := temporaryThemeArchive(buffer.Bytes(), "win-zip")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archivePath)
	info, err := extractAndValidateTheme(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Short != "win-zip" {
		t.Fatalf("extracted short = %q", info.Short)
	}
	if content, err := os.ReadFile(filepath.Join("data", "theme", "win-zip", "dist", "index.html")); err != nil || string(content) != "ok" {
		t.Fatalf("extracted index = %q err=%v", content, err)
	}
}

func TestExtractThemeAcceptsLiteManifestOnly(t *testing.T) {
	t.Chdir(t.TempDir())
	archive := themeArchive(t, map[string]string{
		"Lite-theme.json": `{"name":"LiteOnly","short":"lite-only","configuration":{"type":"managed","data":[]}}`,
		"dist/index.html": "ok",
	})
	archivePath, err := temporaryThemeArchive(archive, "lite-only")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archivePath)
	info, err := extractAndValidateTheme(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Short != "lite-only" {
		t.Fatalf("extracted short = %q", info.Short)
	}
}

func TestPeekThemeAcceptsLegacyManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	archive := themeArchive(t, map[string]string{
		"komari-theme.json": `{"name":"Legacy","short":"legacy-theme","configuration":{"type":"managed","data":[]}}`,
		"dist/index.html":   "ok",
	})
	archivePath, err := temporaryThemeArchive(archive, "legacy-theme")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archivePath)
	info, err := peekThemeFromZip(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Short != "legacy-theme" {
		t.Fatalf("peeked short = %q", info.Short)
	}
}

func TestFailedThemeUpdatePreservesExistingTheme(t *testing.T) {
	t.Chdir(t.TempDir())
	themeDir := filepath.Join("data", "theme", "existing")
	if err := os.MkdirAll(filepath.Join(themeDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"Existing","short":"existing","configuration":{"type":"managed","data":[]}}`
	if err := os.WriteFile(filepath.Join(themeDir, "komari-theme.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "dist", "index.html"), []byte("old theme"), 0o644); err != nil {
		t.Fatal(err)
	}

	invalidArchive := themeArchive(t, map[string]string{
		"komari-theme.json": manifest,
		"dist/app.js":       "new but incomplete",
	})
	archivePath, err := temporaryThemeArchive(invalidArchive, "invalid-theme")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(archivePath)
	if _, err := extractAndValidateTheme(archivePath); err == nil {
		t.Fatal("invalid update unexpectedly succeeded")
	}
	content, err := os.ReadFile(filepath.Join(themeDir, "dist", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old theme" {
		t.Fatalf("existing theme changed after failed update: %q", content)
	}
}

func TestThemeDeletionFallbackRequiresAnotherTheme(t *testing.T) {
	themes := []models.Theme{{Short: "lite-theme"}}
	found, fallback := themeDeletionFallback(themes, "lite-theme")
	if !found || fallback != "" {
		t.Fatalf("single theme deletion plan = (%t, %q)", found, fallback)
	}

	themes = append(themes, models.Theme{Short: "komari-classic"})
	found, fallback = themeDeletionFallback(themes, "lite-theme")
	if !found || fallback != "komari-classic" {
		t.Fatalf("fallback deletion plan = (%t, %q)", found, fallback)
	}
	if found, _ := themeDeletionFallback(themes, "missing"); found {
		t.Fatal("missing theme was reported as installed")
	}
}

func TestDeleteInstalledThemeRollsBackDirectoryAndConfigTogether(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("data", "theme", "target", "dist"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("data", "theme", "target", "dist", "index.html"), []byte("target"), 0o644))
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "theme-delete.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	closeThemeTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&config.ConfigItem{}, &models.ThemeConfiguration{}))
	require.NoError(t, db.Create(&config.ConfigItem{Key: config.ThemeKey, Value: `"target"`}).Error)
	require.NoError(t, db.Create(&models.ThemeConfiguration{Short: "target", Data: `{}`}).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_theme_configuration_delete BEFORE DELETE ON theme_configurations
		BEGIN SELECT RAISE(FAIL, 'delete rejected'); END`).Error)

	err = deleteInstalledTheme(db, "target", "target", "fallback")
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join("data", "theme", "target", "dist", "index.html"))
	require.NoError(t, statErr)
	var item config.ConfigItem
	require.NoError(t, db.First(&item, "key = ?", config.ThemeKey).Error)
	assert.Equal(t, `"target"`, item.Value)
	var configurationCount int64
	require.NoError(t, db.Model(&models.ThemeConfiguration{}).Where("short = ?", "target").Count(&configurationCount).Error)
	assert.Equal(t, int64(1), configurationCount)
	deleted, globErr := filepath.Glob(filepath.Join("data", "theme", ".deleted-target-*"))
	require.NoError(t, globErr)
	assert.Empty(t, deleted)
}

func TestDeleteInstalledThemeCommitsFallbackAndConfigurationRemoval(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("data", "theme", "target", "dist"), 0o755))
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "theme-delete-success.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	closeThemeTestDB(t, db)
	require.NoError(t, db.AutoMigrate(&config.ConfigItem{}, &models.ThemeConfiguration{}))
	require.NoError(t, db.Create(&config.ConfigItem{Key: config.ThemeKey, Value: `"target"`}).Error)
	require.NoError(t, db.Create(&models.ThemeConfiguration{Short: "target", Data: `{}`}).Error)

	require.NoError(t, deleteInstalledTheme(db, "target", "target", "fallback"))
	_, statErr := os.Stat(filepath.Join("data", "theme", "target"))
	assert.True(t, os.IsNotExist(statErr), "theme directory still exists: %v", statErr)
	var item config.ConfigItem
	require.NoError(t, db.First(&item, "key = ?", config.ThemeKey).Error)
	assert.Equal(t, `"fallback"`, item.Value)
	var configurationCount int64
	require.NoError(t, db.Model(&models.ThemeConfiguration{}).Where("short = ?", "target").Count(&configurationCount).Error)
	assert.Zero(t, configurationCount)
}
