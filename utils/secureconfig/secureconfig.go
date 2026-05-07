package secureconfig

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const keyFilePath = "./data/secret.key"

func EncryptString(plaintext string) (string, error) {
	key, err := loadOrCreateKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	payload := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(payload), nil
}

func DecryptString(ciphertext string) (string, error) {
	if strings.TrimSpace(ciphertext) == "" {
		return "", nil
	}

	key, err := loadOrCreateKey()
	if err != nil {
		return "", err
	}

	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}

	nonce := raw[:gcm.NonceSize()]
	data := raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

func loadOrCreateKey() ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("KOMARI_SECRET_KEY")); env != "" {
		return normalizeKey(env)
	}

	if err := os.MkdirAll(filepath.Dir(keyFilePath), 0700); err != nil {
		return nil, err
	}

	if data, err := os.ReadFile(keyFilePath); err == nil {
		return normalizeKey(strings.TrimSpace(string(data)))
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}

	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(keyFilePath, []byte(encoded), 0600); err != nil {
		return nil, err
	}

	return key, nil
}

func normalizeKey(input string) ([]byte, error) {
	if decoded, err := base64.StdEncoding.DecodeString(input); err == nil {
		if len(decoded) == 32 {
			return decoded, nil
		}
	}

	if len(input) >= 16 {
		sum := sha256.Sum256([]byte(input))
		return sum[:], nil
	}

	return nil, fmt.Errorf("invalid secret key length")
}
