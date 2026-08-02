package public

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
