package admin

import (
	"archive/zip"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func createConfigSnapshotFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "komari.db")
	db, err := openSnapshotDatabase(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	statements := []string{
		`CREATE TABLE configs (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE users (uuid TEXT PRIMARY KEY, username TEXT)`,
		`CREATE TABLE sessions (session TEXT PRIMARY KEY, uuid TEXT)`,
		`CREATE TABLE clients (uuid TEXT PRIMARY KEY, name TEXT)`,
		`CREATE TABLE ping_tasks (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE return_route_tasks (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE clipboards (id INTEGER PRIMARY KEY, text TEXT)`,
		`CREATE TABLE ping_loss_notifications (id INTEGER PRIMARY KEY, last_notified TEXT, alert_active INTEGER)`,
		`CREATE TABLE records (client TEXT, value REAL)`,
		`CREATE TABLE metric_points (metric_name TEXT, value REAL)`,
		`CREATE TABLE logs (id INTEGER PRIMARY KEY, message TEXT)`,
		`CREATE TABLE return_route_events (id INTEGER PRIMARY KEY, target TEXT)`,
		`INSERT INTO clients VALUES ('node-a', 'Node A')`,
		`INSERT INTO users VALUES ('user-a', 'admin')`,
		`INSERT INTO sessions VALUES ('session-a', 'user-a')`,
		`INSERT INTO ping_tasks VALUES (1, 'Probe A')`,
		`INSERT INTO return_route_tasks VALUES (1, 'Route A')`,
		`INSERT INTO clipboards VALUES (1, 'secret clipboard content')`,
		`INSERT INTO ping_loss_notifications VALUES (1, '2026-08-01T00:00:00Z', 1)`,
		`INSERT INTO records VALUES ('node-a', 1)`,
		`INSERT INTO metric_points VALUES ('cpu', 2)`,
		`INSERT INTO logs VALUES (1, 'log')`,
		`INSERT INTO return_route_events VALUES (1, 'target')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("prepare fixture with %q: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	return path
}

func TestSanitizeConfigSnapshotKeepsServersAndTasksWithoutHistory(t *testing.T) {
	path := createConfigSnapshotFixture(t)
	if err := sanitizeConfigSnapshot(context.Background(), path, backupScopeConfig); err != nil {
		t.Fatalf("sanitize snapshot: %v", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sanitized snapshot: %v", err)
	}
	defer db.Close()
	for table, want := range map[string]int{
		"users": 1, "clients": 1, "ping_tasks": 1, "return_route_tasks": 1,
		"sessions": 0, "clipboards": 0, "logs": 0, "return_route_events": 0,
	} {
		var got int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + quoteSQLiteIdentifier(table)).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
	var lastNotified any
	var alertActive int
	if err := db.QueryRow(`SELECT last_notified, alert_active FROM ping_loss_notifications WHERE id = 1`).Scan(&lastNotified, &alertActive); err != nil {
		t.Fatalf("read reset notification state: %v", err)
	}
	if lastNotified != nil || alertActive != 0 {
		t.Fatalf("notification runtime state was retained: last=%v active=%d", lastNotified, alertActive)
	}
	for _, table := range []string{"records", "metric_points"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("monitoring table %s remains in config snapshot", table)
		}
	}
	for key, want := range map[string]string{
		"metric_db_driver":        `"sqlite"`,
		"metric_db_dsn":           `"./data/metrics.db"`,
		"metric_migration_target": `"sqlite|./data/metrics.db"`,
	} {
		var got string
		if err := db.QueryRow(`SELECT value FROM configs WHERE key = ?`, key).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestBuildBackupArchiveUsesUpstreamCompatibleRootLayout(t *testing.T) {
	content := t.TempDir()
	if err := os.WriteFile(filepath.Join(content, "komari.db"), []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "metrics.db"), []byte("history"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if err := buildBackupArchive(archive, content, backupScopeFull, timeForBackupTest); err != nil {
		t.Fatalf("build archive: %v", err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer reader.Close()
	found := map[string]bool{}
	for _, entry := range reader.File {
		found[entry.Name] = true
	}
	for _, name := range []string{"komari.db", "metrics.db", "komari-backup-markup"} {
		if !found[name] {
			t.Fatalf("archive missing %s: %#v", name, found)
		}
	}
}

func TestBuildConfigurationArchiveDoesNotContainMetricHistory(t *testing.T) {
	content := t.TempDir()
	if err := os.WriteFile(filepath.Join(content, "komari.db"), []byte("configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "config.zip")
	if err := buildBackupArchive(archive, content, backupScopeConfig, timeForBackupTest); err != nil {
		t.Fatalf("build configuration archive: %v", err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatalf("open configuration archive: %v", err)
	}
	defer reader.Close()
	found := map[string]bool{}
	for _, entry := range reader.File {
		found[entry.Name] = true
	}
	if !found["komari.db"] || !found["komari-backup-markup"] {
		t.Fatalf("configuration archive is not compatible with upstream restore: %#v", found)
	}
	if found["metrics.db"] || found["metrics.db-wal"] || found["metrics.db-shm"] {
		t.Fatalf("configuration archive contains metric history: %#v", found)
	}
}

var timeForBackupTest = func() time.Time {
	return time.Unix(0, 0).UTC()
}()
