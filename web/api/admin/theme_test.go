package admin

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/komari-monitor/komari/database/models"
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
	themes := []models.Theme{{Short: "nezha"}}
	found, fallback := themeDeletionFallback(themes, "nezha")
	if !found || fallback != "" {
		t.Fatalf("single theme deletion plan = (%t, %q)", found, fallback)
	}

	themes = append(themes, models.Theme{Short: "komari-classic"})
	found, fallback = themeDeletionFallback(themes, "nezha")
	if !found || fallback != "komari-classic" {
		t.Fatalf("fallback deletion plan = (%t, %q)", found, fallback)
	}
	if found, _ := themeDeletionFallback(themes, "missing"); found {
		t.Fatal("missing theme was reported as installed")
	}
}
