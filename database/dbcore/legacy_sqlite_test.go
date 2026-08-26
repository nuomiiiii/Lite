package dbcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nuomiiiii/lite/cmd/flags"
)

func TestAdoptLegacyKomariSQLiteRenamesDefaultFiles(t *testing.T) {
	dir := t.TempDir()
	previous := flags.DatabaseFile
	t.Cleanup(func() { flags.DatabaseFile = previous })
	flags.DatabaseFile = filepath.Join(dir, "lite.db")

	if err := os.WriteFile(filepath.Join(dir, "komari.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "komari.db-wal"), []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := adoptLegacyKomariSQLite(); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lite.db")); err != nil {
		t.Fatalf("lite.db missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lite.db-wal")); err != nil {
		t.Fatalf("lite.db-wal missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "komari.db")); !os.IsNotExist(err) {
		t.Fatalf("komari.db should have been renamed, err=%v", err)
	}
	if !KeepLegacyHTTPListen(dir) {
		t.Fatal("adopting komari.db should keep the previous HTTP listen port")
	}
}

func TestAdoptLegacyKomariSQLiteLeavesExistingLiteDB(t *testing.T) {
	dir := t.TempDir()
	previous := flags.DatabaseFile
	t.Cleanup(func() { flags.DatabaseFile = previous })
	flags.DatabaseFile = filepath.Join(dir, "lite.db")

	if err := os.WriteFile(filepath.Join(dir, "lite.db"), []byte("lite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "komari.db"), []byte("komari"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := adoptLegacyKomariSQLite(); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	lite, err := os.ReadFile(filepath.Join(dir, "lite.db"))
	if err != nil {
		t.Fatal(err)
	}
	if string(lite) != "lite" {
		t.Fatalf("existing lite.db overwritten: %q", lite)
	}
	if _, err := os.Stat(filepath.Join(dir, "komari.db")); err != nil {
		t.Fatalf("legacy file should remain when lite.db exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".legacy-http-listen")); !os.IsNotExist(err) {
		t.Fatal("existing lite.db must not write a legacy listen marker")
	}
}

func TestKeepLegacyHTTPListenSeesKomariDatabase(t *testing.T) {
	dir := t.TempDir()
	if KeepLegacyHTTPListen(dir) {
		t.Fatal("empty data dir should use the Lite listen default")
	}
	if err := os.WriteFile(filepath.Join(dir, "komari.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !KeepLegacyHTTPListen(dir) {
		t.Fatal("komari.db should keep HTTP listen on 25774")
	}
	if got := ResolveDefaultHTTPListen(dir); got != defaultLegacyHTTPListen {
		t.Fatalf("resolve = %q, want %q", got, defaultLegacyHTTPListen)
	}
}

func TestResolveDefaultHTTPListenReadsCustomMarker(t *testing.T) {
	dir := t.TempDir()
	if got := ResolveDefaultHTTPListen(dir); got != defaultLiteHTTPListen {
		t.Fatalf("fresh dir = %q, want %q", got, defaultLiteHTTPListen)
	}
	if err := os.WriteFile(filepath.Join(dir, ".legacy-http-listen"), []byte("0.0.0.0:18080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDefaultHTTPListen(dir); got != "0.0.0.0:18080" {
		t.Fatalf("custom marker = %q, want 0.0.0.0:18080", got)
	}
}

func TestAdoptLegacyKomariSQLiteWritesListenMarkerFromFlags(t *testing.T) {
	dir := t.TempDir()
	previousDB := flags.DatabaseFile
	previousListen := flags.Listen
	t.Cleanup(func() {
		flags.DatabaseFile = previousDB
		flags.Listen = previousListen
	})
	flags.DatabaseFile = filepath.Join(dir, "lite.db")
	flags.Listen = "127.0.0.1:19090"
	if err := os.WriteFile(filepath.Join(dir, "komari.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptLegacyKomariSQLite(); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".legacy-http-listen"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(content)); got != "127.0.0.1:19090" {
		t.Fatalf("marker = %q, want the previous listen address", got)
	}
	if got := ResolveDefaultHTTPListen(dir); got != "127.0.0.1:19090" {
		t.Fatalf("resolve after adopt = %q", got)
	}
}

func TestAdoptLegacyKomariSQLiteIgnoresLiteDefaultListen(t *testing.T) {
	dir := t.TempDir()
	previousDB := flags.DatabaseFile
	previousListen := flags.Listen
	t.Cleanup(func() {
		flags.DatabaseFile = previousDB
		flags.Listen = previousListen
	})
	flags.DatabaseFile = filepath.Join(dir, "lite.db")
	flags.Listen = defaultLiteHTTPListen
	if err := os.WriteFile(filepath.Join(dir, "komari.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptLegacyKomariSQLite(); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, ".legacy-http-listen"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(content)); got != defaultLegacyHTTPListen {
		t.Fatalf("marker = %q, want %q so upgrade does not jump to 27777", got, defaultLegacyHTTPListen)
	}
}
