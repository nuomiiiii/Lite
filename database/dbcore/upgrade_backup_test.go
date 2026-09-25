package dbcore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPruneUpgradeBackupsKeepsOnlyTheNewArchive(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "upgrade-20260926-010000.zip")
	older := filepath.Join(dir, "upgrade-20260925-010000.zip")
	partial := filepath.Join(dir, "upgrade-20260925-010000.zip.partial")
	restore := filepath.Join(dir, "pre-restore-20260925-010000.zip")
	snapshot := filepath.Join(dir, "self-update-20260925")
	for _, path := range []string{keep, older, partial, restore} {
		if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(snapshot, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, "Lite"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}

	pruneUpgradeBackups(dir, keep)

	for _, path := range []string{keep, partial, restore, filepath.Join(snapshot, "Lite")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to remain: %v", path, err)
		}
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatalf("older upgrade archive still present: %v", err)
	}
}

func TestPruneUpgradeBackupsKeepsOlderArchivesWhenNewOneIsEmpty(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "upgrade-20260926-010000.zip")
	older := filepath.Join(dir, "upgrade-20260925-010000.zip")
	if err := os.WriteFile(keep, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(older, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	pruneUpgradeBackups(dir, keep)

	if _, err := os.Stat(older); err != nil {
		t.Fatalf("older archive was removed: %v", err)
	}
}
