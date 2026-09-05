package dbcore

import (
	"archive/zip"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nuomiiiii/lite/utils/instancekey"
)

const (
	restorePreviousKeyPrefix = ".lite-restore-key-"
	totpSecretPrefix         = "enc:v1:"
)

func inspectArchiveInstanceKey(zipPath string) (string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("open backup archive: %w", err)
	}
	defer reader.Close()

	var encoded string
	for _, entry := range reader.File {
		name := path.Clean(filepath.ToSlash(entry.Name))
		if !strings.EqualFold(filepath.Base(name), instancekey.FileName) {
			continue
		}
		if name != instancekey.FileName {
			return "", fmt.Errorf("backup archive contains a nested instance key")
		}
		if encoded != "" {
			return "", fmt.Errorf("backup archive contains duplicate instance keys")
		}
		if entry.FileInfo().IsDir() {
			return "", fmt.Errorf("backup instance key is unreadable")
		}
		payload, err := readZipFile(entry)
		if err != nil {
			return "", fmt.Errorf("backup instance key is unreadable")
		}
		trimmed := strings.TrimSpace(string(payload))
		if _, err := instancekey.DecodeEncoded(trimmed); err != nil {
			return "", err
		}
		encoded = trimmed
	}
	return encoded, nil
}

func readZipFile(entry *zip.File) ([]byte, error) {
	rc, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 1<<12))
}

func instanceKeyForArchive(srcDir string) (string, error) {
	dest, inside, err := restoreKeyPlan(srcDir)
	if err != nil {
		return "", err
	}
	if inside {
		encoded, err := instancekey.ReadEncodedFrom(dest)
		if os.IsNotExist(err) {
			return "", nil
		}
		return encoded, err
	}
	encoded, err := instancekey.ReadEncoded()
	if os.IsNotExist(err) {
		return "", nil
	}
	return encoded, err
}

func restoreKeyPlan(dataDir string) (dest string, inside bool, err error) {
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return "", false, err
	}
	configured := strings.TrimSpace(os.Getenv("LITE_INSTANCE_KEY_FILE"))
	if configured == "" {
		return filepath.Join(absData, instancekey.FileName), true, nil
	}
	absKey, err := filepath.Abs(configured)
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(absData, absKey)
	if err != nil {
		return absKey, false, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return absKey, false, nil
	}
	return absKey, true, nil
}

func stageRestoredInstanceKey(stageDir, dataDir, encoded string) error {
	if encoded == "" {
		return nil
	}
	dest, inside, err := restoreKeyPlan(dataDir)
	if err != nil {
		return err
	}
	if !inside {
		return nil
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absData, dest)
	if err != nil {
		return err
	}
	return instancekey.WriteEncodedTo(filepath.Join(stageDir, rel), encoded)
}

func backupExternalInstanceKey(dataDir string) (string, error) {
	dest, inside, err := restoreKeyPlan(dataDir)
	if err != nil {
		return "", err
	}
	if inside {
		return "", nil
	}
	encoded, err := instancekey.ReadEncodedFrom(dest)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(dataDir)
	file, err := os.CreateTemp(parent, restorePreviousKeyPrefix+"*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.WriteString(encoded); err != nil {
		file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func publishExternalInstanceKey(dataDir, encoded, previousPath string) error {
	if encoded == "" {
		return nil
	}
	_, inside, err := restoreKeyPlan(dataDir)
	if err != nil {
		return err
	}
	if inside {
		return nil
	}
	if err := instancekey.WriteEncoded(encoded); err != nil {
		if previousPath != "" {
			_ = restoreExternalInstanceKey(previousPath)
		}
		return err
	}
	return nil
}

func restoreExternalInstanceKey(previousPath string) error {
	if previousPath == "" {
		return nil
	}
	encoded, err := instancekey.ReadEncodedFrom(previousPath)
	if err != nil {
		return err
	}
	return instancekey.WriteEncoded(encoded)
}

func validateStagedBackupSecrets(dbPath, encodedKey string) error {
	var key []byte
	if strings.TrimSpace(encodedKey) != "" {
		decoded, err := instancekey.DecodeEncoded(encodedKey)
		if err != nil {
			return err
		}
		key = decoded
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return fmt.Errorf("open restored database: %w", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		return fmt.Errorf("open restored database: %w", err)
	}
	exists, err := sqliteTableExists(db, "users")
	if err != nil {
		return fmt.Errorf("inspect restored database: %w", err)
	}
	if !exists {
		return nil
	}
	rows, err := db.Query(`SELECT two_factor FROM users`)
	if err != nil {
		return fmt.Errorf("inspect restored 2FA secrets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var stored sql.NullString
		if err := rows.Scan(&stored); err != nil {
			return fmt.Errorf("inspect restored 2FA secrets: %w", err)
		}
		secret := strings.TrimSpace(stored.String)
		if secret == "" || !strings.HasPrefix(secret, totpSecretPrefix) {
			continue
		}
		if len(key) == 0 {
			return fmt.Errorf("backup is missing a matching instance key")
		}
		if err := verifyStagedTOTPSecret(secret, key); err != nil {
			return fmt.Errorf("backup instance key does not match the restored database")
		}
	}
	return rows.Err()
}

func verifyStagedTOTPSecret(stored string, key []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, totpSecretPrefix))
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(raw) < gcm.NonceSize() {
		return fmt.Errorf("2FA secret is unreadable")
	}
	nonce, data := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	_, err = gcm.Open(nil, nonce, data, nil)
	return err
}

func sqliteTableExists(db *sql.DB, name string) (bool, error) {
	var found sql.NullString
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found.Valid && found.String == name, nil
}
