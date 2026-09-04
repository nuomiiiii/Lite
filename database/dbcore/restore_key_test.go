package dbcore

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nuomiiiii/lite/utils/instancekey"
)

func validInstanceKey(t *testing.T) (string, []byte) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key), key
}

func encryptTOTPForTest(t *testing.T, key []byte, secret string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	payload := gcm.Seal(nonce, nonce, []byte(secret), nil)
	return "enc:v1:" + base64.StdEncoding.EncodeToString(payload)
}

func createRestoreSQLiteWithUser(t *testing.T, path, twoFactor string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE users (uuid TEXT, two_factor TEXT); INSERT INTO users VALUES ('u1', ?)`, twoFactor)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeRestoreArchiveBytes(t *testing.T, archivePath string, files map[string][]byte) {
	t.Helper()
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := files["lite-backup-markup"]; !ok {
		if _, ok := files["komari-backup-markup"]; !ok {
			marker, err := writer.Create("lite-backup-markup")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := marker.Write([]byte("backup")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
}

func sqliteBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRestoreRejectsEncryptedSecretsWithoutInstanceKey(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	_, key := validInstanceKey(t)
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLiteWithUser(t, newDB, encryptTOTPForTest(t, key, "JBSWY3DPEHPK3PXP"))
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db": sqliteBytes(t, newDB),
	})
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("restore accepted encrypted 2FA without an instance key")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "lite.db")); err != nil {
		t.Fatal("live database was replaced")
	}
}

func TestRestoreRejectsMismatchedInstanceKey(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	_, key := validInstanceKey(t)
	wrong, _ := validInstanceKey(t)
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLiteWithUser(t, newDB, encryptTOTPForTest(t, key, "JBSWY3DPEHPK3PXP"))
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db":           sqliteBytes(t, newDB),
		"lite-instance.key": []byte(wrong),
	})
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("restore accepted a mismatched instance key")
	}
}

func TestRestoreMatchingInstanceKeyWritesIntoDataDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	encoded, key := validInstanceKey(t)
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLiteWithUser(t, newDB, encryptTOTPForTest(t, key, "JBSWY3DPEHPK3PXP"))
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db":           sqliteBytes(t, newDB),
		"lite-instance.key": []byte(encoded),
	})
	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("restore matching key: %v", err)
	}
	if err := restore.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := instancekey.ReadEncodedFrom(filepath.Join(dataDir, instancekey.FileName))
	if err != nil || got != encoded {
		t.Fatalf("restored key = %q err=%v", got, err)
	}
}

func TestRestoreRejectsNestedInstanceKey(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	encoded, _ := validInstanceKey(t)
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDB, "new")
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db":                  sqliteBytes(t, newDB),
		"nested/lite-instance.key": []byte(encoded),
	})
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("nested instance key was accepted")
	}
}

func TestRestoreBusyDataDirStillWritesInstanceKey(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	encoded, key := validInstanceKey(t)
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLiteWithUser(t, newDB, encryptTOTPForTest(t, key, "JBSWY3DPEHPK3PXP"))
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db":           sqliteBytes(t, newDB),
		"lite-instance.key": []byte(encoded),
	})
	previous := renameDirectory
	t.Cleanup(func() { renameDirectory = previous })
	renameDirectory = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EBUSY}
	}
	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("docker-style restore: %v", err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("data directory must remain in place: %v", err)
	}
	if err := restore.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := instancekey.ReadEncodedFrom(filepath.Join(dataDir, instancekey.FileName))
	if err != nil || got != encoded {
		t.Fatalf("restored key on busy volume = %q err=%v", got, err)
	}
}

func TestRestoreExternalInstanceKeyRollsBackWithDatabase(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldEncoded, _ := validInstanceKey(t)
	outside := filepath.Join(root, "secrets", instancekey.FileName)
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(oldEncoded), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LITE_INSTANCE_KEY_FILE", outside)
	instancekey.Reload()
	t.Cleanup(instancekey.Reload)
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newEncoded, key := validInstanceKey(t)
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLiteWithUser(t, newDB, encryptTOTPForTest(t, key, "JBSWY3DPEHPK3PXP"))
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db":           sqliteBytes(t, newDB),
		"lite-instance.key": []byte(newEncoded),
	})
	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("external key restore: %v", err)
	}
	got, err := instancekey.ReadEncodedFrom(outside)
	if err != nil || got != newEncoded {
		t.Fatalf("published external key = %q err=%v", got, err)
	}
	if err := restore.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err = instancekey.ReadEncodedFrom(outside)
	if err != nil || got != oldEncoded {
		t.Fatalf("rolled back external key = %q err=%v", got, err)
	}
}

func TestZipDirectoryIncludesCanonicalInstanceKey(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, _ := validInstanceKey(t)
	if err := os.WriteFile(filepath.Join(dataDir, "lite.db"), []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, instancekey.FileName), []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "nested", instancekey.FileName), []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "backup.zip")
	if err := zipDirectoryExcluding(dataDir, zipPath, nil); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	count := 0
	for _, entry := range reader.File {
		if strings.EqualFold(filepath.Base(entry.Name), instancekey.FileName) {
			count++
			if entry.Name != instancekey.FileName {
				t.Fatalf("nested key in archive: %s", entry.Name)
			}
		}
	}
	if count != 1 {
		t.Fatalf("instance key entries = %d, want 1", count)
	}
}

func TestValidateStagedBackupSecretsRejectsNonSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lite.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedBackupSecrets(path, ""); err == nil {
		t.Fatal("non-SQLite file was accepted")
	}
}

func TestValidateStagedBackupSecretsRejectsMalformedSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lite.db")
	payload := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0xff}, 256)...)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedBackupSecrets(path, ""); err == nil {
		t.Fatal("malformed SQLite was accepted")
	}
}

func TestValidateStagedBackupSecretsAllowsMissingUsersTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lite.db")
	createRestoreSQLite(t, path, "legacy")
	if err := validateStagedBackupSecrets(path, ""); err != nil {
		t.Fatalf("legacy database without users: %v", err)
	}
}

func TestValidateStagedBackupSecretsAcceptsEncryptedTwoFactor(t *testing.T) {
	encoded, key := validInstanceKey(t)
	path := filepath.Join(t.TempDir(), "lite.db")
	createRestoreSQLiteWithUser(t, path, encryptTOTPForTest(t, key, "JBSWY3DPEHPK3PXP"))
	if err := validateStagedBackupSecrets(path, encoded); err != nil {
		t.Fatalf("encrypted 2FA database: %v", err)
	}
}

func TestValidateStagedBackupSecretsFailsWhenTwoFactorColumnMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lite.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (uuid TEXT); INSERT INTO users VALUES ('u1')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedBackupSecrets(path, ""); err == nil {
		t.Fatal("users table without two_factor was accepted")
	}
}

func TestRestoreStagedBackupRejectsNonSQLiteAtStaging(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db": []byte("this is not a sqlite database"),
	})
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("restore accepted a non-SQLite backup")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "lite.db")); err != nil {
		t.Fatal("live database was replaced")
	}
}

func TestRestoreStagedBackupRejectsMalformedSQLiteAtStaging(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	payload := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0xff}, 256)...)
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db": payload,
	})
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("restore accepted a malformed SQLite backup")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "lite.db")); err != nil {
		t.Fatal("live database was replaced")
	}
}

func TestRestoreStagedBackupRejectsMalformedMetricsDBAtStaging(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDB, "legacy")
	payload := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0xff}, 256)...)
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db":    sqliteBytes(t, newDB),
		"metrics.db": payload,
	})
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("restore accepted a malformed metrics.db")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "lite.db")); err != nil {
		t.Fatal("live database was replaced")
	}
}

func TestValidateRestoredSQLiteRejectsCorruptDataPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.db")
	createPopulatedRestoreSQLite(t, path)
	assertSQLiteCatalogReadable(t, path)
	corruptLaterSQLitePage(t, path)
	assertSQLiteCatalogReadable(t, path)
	if err := validateRestoredSQLite(path, "metrics.db"); err == nil {
		t.Fatal("validateRestoredSQLite accepted a database with corrupt data pages")
	}
}

func TestRestoreStagedBackupRejectsCorruptMetricsDataPagesAtStaging(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDB, "legacy")
	metricsDB := filepath.Join(root, "metrics.db")
	createPopulatedRestoreSQLite(t, metricsDB)
	assertSQLiteCatalogReadable(t, metricsDB)
	corruptLaterSQLitePage(t, metricsDB)
	assertSQLiteCatalogReadable(t, metricsDB)
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db":    sqliteBytes(t, newDB),
		"metrics.db": sqliteBytes(t, metricsDB),
	})
	if _, err := restoreStagedBackup(dataDir); err == nil {
		t.Fatal("restore accepted a metrics.db with corrupt data pages")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "lite.db")); err != nil {
		t.Fatal("live database was replaced")
	}
}

func TestRestoreStagedBackupAllowsLegacyDatabaseWithoutUsers(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createRestoreSQLite(t, filepath.Join(dataDir, "lite.db"), "old")
	newDB := filepath.Join(root, "new.db")
	createRestoreSQLite(t, newDB, "legacy")
	writeRestoreArchiveBytes(t, filepath.Join(dataDir, "backup.zip"), map[string][]byte{
		"lite.db": sqliteBytes(t, newDB),
	})
	restore, err := restoreStagedBackup(dataDir)
	if err != nil {
		t.Fatalf("legacy restore without users: %v", err)
	}
	if err := restore.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createPopulatedRestoreSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE state (id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	payload := strings.Repeat("m", 256)
	for i := 0; i < 200; i++ {
		if _, err := tx.Exec(`INSERT INTO state(value) VALUES (?)`, payload); err != nil {
			tx.Rollback()
			db.Close()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertSQLiteCatalogReadable(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", sqliteReadOnlyDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("sqlite catalog ping: %v", err)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		t.Fatalf("sqlite_master should still be readable: %v", err)
	}
	if tables == 0 {
		t.Fatal("sqlite_master is empty")
	}
}

func corruptLaterSQLitePage(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 18 {
		t.Fatal("sqlite file too small")
	}
	pageSize := int(binary.BigEndian.Uint16(data[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || len(data) < pageSize*2 {
		t.Fatalf("need at least two sqlite pages, size=%d page=%d", len(data), pageSize)
	}
	pageIndex := 1
	offset := pageIndex * pageSize
	data[offset] = 0xff
	if offset+3 < len(data) {
		data[offset+1] = 0xff
		data[offset+2] = 0xff
		data[offset+3] = 0xff
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
