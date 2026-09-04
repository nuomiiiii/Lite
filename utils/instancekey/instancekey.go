package instancekey

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const (
	FileName    = "lite-instance.key"
	defaultPath = "./data/" + FileName
	keyByteSize = 32
	keyFileMode = 0o600
	keyDirMode  = 0o700
	envPathName = "LITE_INSTANCE_KEY_FILE"
)

var (
	mu     sync.RWMutex
	cached []byte
	loaded bool
)

// Path returns the instance key file path. The default is beside lite.db in
// data/, including Docker volume mounts. LITE_INSTANCE_KEY_FILE overrides it.
func Path() string {
	if env := strings.TrimSpace(os.Getenv(envPathName)); env != "" {
		return env
	}
	return defaultPath
}

func ResetForTest() {
	Reload()
}

func Reload() {
	mu.Lock()
	cached = nil
	loaded = false
	mu.Unlock()
}

func SetupTempFileForTest() func() {
	dir, err := os.MkdirTemp("", "lite-instance-key")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv(envPathName, filepath.Join(dir, FileName))
	Reload()
	return func() {
		_ = os.RemoveAll(dir)
		_ = os.Unsetenv(envPathName)
		Reload()
	}
}

// DecodeEncoded validates a backup/key-file payload: Base64 of exactly 32 bytes.
func DecodeEncoded(encoded string) ([]byte, error) {
	return decodeKey(strings.TrimSpace(encoded))
}

// ReadEncoded returns the on-disk payload after validating it. Missing files
// return os.ErrNotExist. The raw key bytes are never included in errors.
func ReadEncoded() (string, error) {
	return ReadEncodedFrom(Path())
}

func ReadEncodedFrom(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	encoded := strings.TrimSpace(string(data))
	if _, err := decodeKey(encoded); err != nil {
		return "", err
	}
	return encoded, nil
}

// WriteEncoded writes a validated payload to Path() and drops the in-process cache.
func WriteEncoded(encoded string) error {
	if err := WriteEncodedTo(Path(), encoded); err != nil {
		return err
	}
	Reload()
	return nil
}

// WriteEncodedTo writes a validated payload to path using a same-directory
// temp file. Docker bind mounts that reject rename fall back to in-place copy.
func WriteEncodedTo(path, encoded string) error {
	encoded = strings.TrimSpace(encoded)
	if _, err := decodeKey(encoded); err != nil {
		return err
	}
	return replaceFile(path, encoded)
}

// Load returns the existing instance key. Missing or unreadable keys fail closed.
func Load() ([]byte, error) {
	mu.RLock()
	if loaded && len(cached) == keyByteSize {
		key := append([]byte(nil), cached...)
		mu.RUnlock()
		return key, nil
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if loaded && len(cached) == keyByteSize {
		return append([]byte(nil), cached...), nil
	}
	encoded, err := ReadEncodedFrom(Path())
	if err != nil {
		return nil, err
	}
	key, err := decodeKey(encoded)
	if err != nil {
		return nil, err
	}
	cached = key
	loaded = true
	return append([]byte(nil), key...), nil
}

// LoadOrCreate loads the key or writes a new 32-byte key with 0600 permissions.
func LoadOrCreate() ([]byte, error) {
	key, err := Load()
	if err == nil {
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	return createLocked()
}

func createLocked() ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	if loaded && len(cached) == keyByteSize {
		return append([]byte(nil), cached...), nil
	}
	key := make([]byte, keyByteSize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := replaceFile(Path(), encoded); err != nil {
		return nil, err
	}
	cached = key
	loaded = true
	return append([]byte(nil), key...), nil
}

func decodeKey(input string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		return nil, fmt.Errorf("instance key is unreadable")
	}
	if len(decoded) != keyByteSize {
		return nil, fmt.Errorf("instance key is unreadable")
	}
	return decoded, nil
}

func replaceFile(path, encoded string) error {
	if err := os.MkdirAll(filepath.Dir(path), keyDirMode); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".lite-instance-key-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if _, err := temp.WriteString(encoded); err != nil {
		temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temp.Chmod(keyFileMode); err != nil {
		temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err == nil {
		return nil
	} else if !shouldFallbackFileReplace(err) {
		_ = os.Remove(tempPath)
		return err
	}
	if err := copyOverFile(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	_ = os.Remove(tempPath)
	return nil
}

func shouldFallbackFileReplace(err error) bool {
	if err == nil {
		return false
	}
	var link *os.LinkError
	if errors.As(err, &link) {
		err = link.Err
	}
	if errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV) || errors.Is(err, os.ErrExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "busy") ||
		strings.Contains(msg, "file exists") ||
		strings.Contains(msg, "cannot replace") ||
		strings.Contains(msg, "cross-device")
}

func copyOverFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, keyFileMode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(keyFileMode); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
