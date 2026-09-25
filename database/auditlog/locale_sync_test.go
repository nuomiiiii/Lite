package auditlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Search text is stored when the log is written, so the Go tables have to
// match the four admin locales. The locale files and auditLogMessage.ts are
// the source; this test fails when a label or sentence drifts.
func TestCatalogMatchesAdminLocales(t *testing.T) {
	root := localeRoot(t)
	if root == "" {
		t.Skip("Lite-web locales are not next to this module")
	}
	locales := loadLocales(t, root)
	labels := parseTSMap(t, filepath.Join(root, "..", "..", "pages", "admin", "auditLogMessage.ts"), "SETTING_LABEL")
	ops := parseTSMap(t, filepath.Join(root, "..", "..", "pages", "admin", "auditLogMessage.ts"), "FILE_OP")

	if len(labels) != len(settingLabels) {
		t.Fatalf("setting label count: ui %d, catalog %d", len(labels), len(settingLabels))
	}
	for key, path := range labels {
		got, ok := settingLabels[key]
		if !ok {
			t.Errorf("catalog missing setting %s", key)
			continue
		}
		assertFour(t, "setting "+key, path, got, locales)
	}
	for key := range settingLabels {
		if _, ok := labels[key]; !ok {
			t.Errorf("ui map missing setting %s", key)
		}
	}

	if len(ops) != len(fileOpLabels) {
		t.Fatalf("file op count: ui %d, catalog %d", len(ops), len(fileOpLabels))
	}
	for key, path := range ops {
		got, ok := fileOpLabels[key]
		if !ok {
			t.Errorf("catalog missing file op %s", key)
			continue
		}
		assertFour(t, "file op "+key, path, got, locales)
	}

	for key, got := range phrases {
		assertFour(t, key, key, got, locales)
	}
	auditKeys := localeAuditKeys(locales[0])
	for _, key := range auditKeys {
		if _, ok := phrases[key]; !ok {
			t.Errorf("catalog missing phrase %s", key)
		}
	}
	assertFour(t, "none", "common.none", noneLabels, locales)
}

func assertFour(t *testing.T, name, path string, got [4]string, locales []map[string]any) {
	t.Helper()
	for i, doc := range locales {
		want, ok := lookupString(doc, path)
		if !ok {
			t.Errorf("%s: %s missing in locale %d", name, path, i)
			continue
		}
		if want != got[i] {
			t.Errorf("%s locale %d:\n  ui:      %s\n  catalog: %s", name, i, want, got[i])
		}
	}
}

func localeAuditKeys(doc map[string]any) []string {
	audit, _ := doc["audit"].(map[string]any)
	var keys []string
	for key, value := range audit {
		if _, ok := value.(string); ok {
			keys = append(keys, "audit."+key)
		}
	}
	return keys
}

func lookupString(doc map[string]any, path string) (string, bool) {
	var cur any = doc
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur, ok = obj[part]
		if !ok {
			return "", false
		}
	}
	text, ok := cur.(string)
	return text, ok
}

func loadLocales(t *testing.T, root string) []map[string]any {
	t.Helper()
	names := []string{"zh_CN.json", "en.json", "ja_JP.json", "zh_TW.json"}
	out := make([]map[string]any, len(names))
	for i, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func localeRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("missing caller path")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "Lite-web", "src", "i18n", "locales")
	if _, err := os.Stat(root); err != nil {
		return ""
	}
	return root
}

func parseTSMap(t *testing.T, path, name string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?s)const ` + name + `: Record<string, string> = \{(.+?)\n\};`)
	match := pattern.FindSubmatch(data)
	if match == nil {
		t.Fatalf("missing %s in %s", name, path)
	}
	entry := regexp.MustCompile(`(?m)^\s*(?:"([^"]+)"|([A-Za-z0-9_]+)):\s*"([^"]+)"`)
	out := map[string]string{}
	for _, item := range entry.FindAllSubmatch(match[1], -1) {
		key := string(item[1])
		if key == "" {
			key = string(item[2])
		}
		out[key] = string(item[3])
	}
	if len(out) == 0 {
		t.Fatalf("parsed no entries from %s", name)
	}
	return out
}
