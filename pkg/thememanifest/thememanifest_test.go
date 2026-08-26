package thememanifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootNameAcceptsRootManifestsOnly(t *testing.T) {
	cases := map[string]string{
		File:                    File,
		LegacyFile:              LegacyFile,
		"./Lite-theme.json":     File,
		"./komari-theme.json":   LegacyFile,
		"dist/Lite-theme.json":  "",
		"foo/komari-theme.json": "",
		"theme.json":            "",
	}
	for name, want := range cases {
		if got := RootName(name); got != want {
			t.Fatalf("RootName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFindInDirPrefersLiteManifest(t *testing.T) {
	dir := t.TempDir()
	litePath := filepath.Join(dir, File)
	legacyPath := filepath.Join(dir, LegacyFile)
	if err := os.WriteFile(legacyPath, []byte(`{"name":"legacy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := FindInDir(dir); !ok || got != legacyPath {
		t.Fatalf("legacy-only FindInDir = (%q, %t)", got, ok)
	}
	if err := os.WriteFile(litePath, []byte(`{"name":"lite"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := FindInDir(dir); !ok || got != litePath {
		t.Fatalf("prefer-lite FindInDir = (%q, %t)", got, ok)
	}
}
