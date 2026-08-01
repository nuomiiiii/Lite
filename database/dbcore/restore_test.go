package dbcore

import (
	"archive/zip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func createRestoreSQLite(t *testing.T, path, value string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE state (value TEXT); INSERT INTO state VALUES (?)`, value); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeRestoreArchive(t *testing.T, archivePath, databasePath string) {
	t.Helper()
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	databaseEntry, err := writer.Create("komari.db")
	if err != nil {
		t.Fatal(err)
	}
	database, err := os.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(databaseEntry, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	database.Close()
	marker, err := writer.Create("komari-backup-markup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := marker.Write([]byte("backup")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreStagedBackupReplacesDataOnlyAfterValidation(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "komari.db"), "old")
	newDatabase := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDatabase, "new")
	writeRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), newDatabase)

	if err := restoreStagedBackup(dataDir); err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "komari.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM state`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "new" {
		t.Fatalf("restored value = %q, want new", value)
	}
	archives, err := filepath.Glob(filepath.Join(root, "backup", "pre-restore-*.zip"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("pre-restore archives = %#v, err=%v", archives, err)
	}
}

func TestRestoreStagedBackupKeepsCurrentDataWhenArchiveIsInvalid(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(dataDir, "current.txt")
	if err := os.WriteFile(current, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "backup.zip"), []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("invalid archive was restored")
	}
	content, err := os.ReadFile(current)
	if err != nil || string(content) != "keep" {
		t.Fatalf("current data changed: %q, err=%v", content, err)
	}
}
