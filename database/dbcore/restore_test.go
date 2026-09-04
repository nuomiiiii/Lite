package dbcore

import (
	"archive/zip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/nuomiiiii/lite/cmd/flags"
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
	writeNamedRestoreArchive(t, archivePath, "lite.db", databasePath)
}

func writeNamedRestoreArchive(t *testing.T, archivePath, zipName, databasePath string) {
	writeRestoreArchiveFiles(t, archivePath, map[string]string{zipName: databasePath})
}

func writeKomariFullRestoreArchive(t *testing.T, archivePath, databasePath, metricsPath string) {
	writeRestoreArchiveFiles(t, archivePath, map[string]string{
		"komari.db":  databasePath,
		"metrics.db": metricsPath,
	})
}

func writeRestoreArchiveFiles(t *testing.T, archivePath string, files map[string]string) {
	t.Helper()
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for zipName, filePath := range files {
		entry, err := writer.Create(zipName)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(filePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(entry, file); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()
	}
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
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDatabase := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDatabase, "new")
	writeRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), newDatabase)

	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	if restore == nil {
		t.Fatal("restore transaction was not created")
	}
	if err := restore.Commit(); err != nil {
		t.Fatalf("commit restore: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "lite.db"))
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
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("invalid archive was restored")
	}
	content, err := os.ReadFile(current)
	if err != nil || string(content) != "keep" {
		t.Fatalf("current data changed: %q, err=%v", content, err)
	}
}

func TestRestoreStagedBackupRollsBackAfterStartupFailure(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDatabase := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDatabase, "new")
	writeRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), newDatabase)

	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("publish staged restore: %v", err)
	}
	if err := restore.Rollback(); err != nil {
		t.Fatalf("rollback staged restore: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "lite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM state`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "old" {
		t.Fatalf("rolled back value = %q, want old", value)
	}
	failed, err := filepath.Glob(filepath.Join(dataDir, "backup.failed-*.zip"))
	if err != nil || len(failed) != 1 {
		t.Fatalf("failed restore archive = %#v, err=%v", failed, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "backup.zip")); !os.IsNotExist(err) {
		t.Fatalf("failed package would be retried: %v", err)
	}
}

func TestCloseRollsBackPendingRestoreWithoutDatabaseHandle(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDatabase := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDatabase, "new")
	writeRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), newDatabase)

	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("publish staged restore: %v", err)
	}
	previousInstance, previousRestore := instance, pendingRestore
	instance, pendingRestore = nil, restore
	t.Cleanup(func() {
		instance, pendingRestore = previousInstance, previousRestore
	})
	if err := Close(); err != nil {
		t.Fatalf("close without database handle: %v", err)
	}

	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "lite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM state`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "old" {
		t.Fatalf("rolled back value = %q, want old", value)
	}
}

func TestInterruptedRestoreRollsBackBeforeCommit(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDatabase := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDatabase, "new")
	writeRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), newDatabase)

	if _, err := restoreStagedBackup(dataDir); err != nil {
		t.Fatalf("publish staged restore: %v", err)
	}
	if err := recoverInterruptedRestore(dataDir); err != nil {
		t.Fatalf("recover interrupted restore: %v", err)
	}

	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "lite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM state`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "old" {
		t.Fatalf("recovered value = %q, want old", value)
	}
	interrupted, err := filepath.Glob(filepath.Join(dataDir, "backup.interrupted-*.zip"))
	if err != nil || len(interrupted) != 1 {
		t.Fatalf("interrupted restore archive = %#v, err=%v", interrupted, err)
	}
}

func TestInterruptedRestoreKeepsDataAfterCommitMarker(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDatabase := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDatabase, "new")
	writeRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), newDatabase)

	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("publish staged restore: %v", err)
	}
	if err := writeRestoreMarker(restore.committedMarker, []byte("committed\n")); err != nil {
		t.Fatalf("write committed marker: %v", err)
	}
	if err := recoverInterruptedRestore(dataDir); err != nil {
		t.Fatalf("finish interrupted commit: %v", err)
	}

	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "lite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM state`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "new" {
		t.Fatalf("committed value = %q, want new", value)
	}
	if _, err := os.Stat(restore.previousDir); !os.IsNotExist(err) {
		t.Fatalf("previous directory still exists: %v", err)
	}
}

func TestRestoreStagedBackupAdoptsKomariDatabase(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDatabase := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDatabase, "from-komari")
	writeNamedRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), "komari.db", newDatabase)

	previous := flags.DatabaseFile
	t.Cleanup(func() { flags.DatabaseFile = previous })
	flags.DatabaseFile = filepath.Join(dataDir, "lite.db")

	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("restore komari.db backup: %v", err)
	}
	if err := restore.Commit(); err != nil {
		t.Fatalf("commit restore: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "lite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM state`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "from-komari" {
		t.Fatalf("restored value = %q, want from-komari", value)
	}
}

func TestRestoreStagedBackupAdoptsKomariFullBackupWithMetrics(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	writeFakeSQLite(t, filepath.Join(dataDir, "metrics.db"), "old-metrics")
	mainDB := filepath.Join(root, "komari.db")
	metricsDB := filepath.Join(root, "metrics.db")
	writeFakeSQLite(t, mainDB, "from-komari")
	writeFakeSQLite(t, metricsDB, "from-komari-metrics")
	writeKomariFullRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), mainDB, metricsDB)

	previous := flags.DatabaseFile
	t.Cleanup(func() { flags.DatabaseFile = previous })
	flags.DatabaseFile = filepath.Join(dataDir, "lite.db")

	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("restore komari 1.4 full backup: %v", err)
	}
	if err := restore.Commit(); err != nil {
		t.Fatalf("commit restore: %v", err)
	}
	for name, want := range map[string]string{
		"lite.db":    "from-komari",
		"metrics.db": "from-komari-metrics",
	} {
		got, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if !strings.Contains(string(got), want) {
			t.Fatalf("restored %s = %q, want payload %q", name, got, want)
		}
	}
}

func TestRestoreStagedBackupFallsBackWhenDataDirCannotBeRenamed(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	if err := os.WriteFile(filepath.Join(dataDir, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	newDatabase := filepath.Join(root, "new.db")
	writeFakeSQLite(t, newDatabase, "new")
	writeRestoreArchive(t, filepath.Join(dataDir, "backup.zip"), newDatabase)

	previous := renameDirectory
	t.Cleanup(func() { renameDirectory = previous })
	renameDirectory = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EBUSY}
	}

	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("restore backup on busy data dir: %v", err)
	}
	if restore == nil {
		t.Fatal("restore transaction was not created")
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data directory must remain in place: %v", err)
	}
	if err := restore.Commit(); err != nil {
		t.Fatalf("commit restore: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "lite.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "new") {
		t.Fatalf("restored lite.db = %q, want payload new", content)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "keep.txt")); !os.IsNotExist(err) {
		t.Fatal("previous files should have been moved aside")
	}
}

func writeFakeSQLite(t *testing.T, path, payload string) {
	t.Helper()
	createRestoreSQLite(t, path, payload)
}
