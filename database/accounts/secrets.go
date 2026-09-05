package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/utils/instancekey"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const totpSecretPrefix = "enc:v1:"

func hashSessionToken(plain string) (string, error) {
	key, err := instancekey.LoadOrCreate()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func looksHashedSession(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func encryptTOTPSecret(secret string) (string, error) {
	key, err := instancekey.LoadOrCreate()
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
	payload := gcm.Seal(nonce, nonce, []byte(secret), nil)
	return totpSecretPrefix + base64.StdEncoding.EncodeToString(payload), nil
}

func decryptTOTPSecret(stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if !strings.HasPrefix(stored, totpSecretPrefix) {
		return stored, nil
	}
	key, err := instancekey.Load()
	if err != nil {
		return "", fmt.Errorf("instance key is required to decrypt 2FA secrets")
	}
	return openTOTPSecret(stored, key)
}

func verifyTOTPSecretWithKey(stored string, key []byte) error {
	_, err := openTOTPSecret(stored, key)
	return err
}

func openTOTPSecret(stored string, key []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, totpSecretPrefix))
	if err != nil {
		return "", fmt.Errorf("2FA secret is unreadable")
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
		return "", fmt.Errorf("2FA secret is unreadable")
	}
	nonce, data := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", fmt.Errorf("2FA secret is unreadable")
	}
	return string(plain), nil
}

func totpOpts() totp.ValidateOpts {
	return totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	}
}

func matchingTOTPCounter(secret, code string, lastAccepted int64, at time.Time) (int64, bool) {
	opts := totpOpts()
	period := int64(opts.Period)
	if period <= 0 {
		period = 30
	}
	base := at.Unix() / period
	skew := int64(opts.Skew)
	for delta := -skew; delta <= skew; delta++ {
		counter := base + delta
		if counter <= lastAccepted {
			continue
		}
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(counter*period, 0), opts)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return counter, true
		}
	}
	return 0, false
}

func consumeTOTPCounter(uuid, code, secret string, lastAccepted int64) (bool, error) {
	counter, ok := matchingTOTPCounter(secret, code, lastAccepted, time.Now())
	if !ok {
		return false, nil
	}
	db := dbcore.GetDBInstance()
	result := db.Model(&models.User{}).
		Where("uuid = ? AND two_factor_counter < ?", uuid, counter).
		Updates(map[string]interface{}{
			"two_factor_counter": counter,
			"updated_at":         time.Now().UTC(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// MigrateStoredSecrets encrypts plaintext TOTP secrets and hashes leftover
// plaintext session tokens. Restore detection wipes sessions separately.
func MigrateStoredSecrets() error {
	hasEncrypted, err := hasEncryptedTOTPSecrets()
	if err != nil {
		return err
	}
	if hasEncrypted {
		if _, err := instancekey.Load(); err != nil {
			return fmt.Errorf("instance key is required to decrypt 2FA secrets")
		}
	} else if _, err := instancekey.LoadOrCreate(); err != nil {
		return err
	}
	db := dbcore.GetDBInstance()
	var users []models.User
	if err := db.Select("uuid", "two_factor").Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		if user.TwoFactor == "" || strings.HasPrefix(user.TwoFactor, totpSecretPrefix) {
			if strings.HasPrefix(user.TwoFactor, totpSecretPrefix) {
				if _, err := decryptTOTPSecret(user.TwoFactor); err != nil {
					return fmt.Errorf("decrypt stored 2FA secret: %w", err)
				}
			}
			continue
		}
		encrypted, err := encryptTOTPSecret(user.TwoFactor)
		if err != nil {
			return err
		}
		if err := db.Model(&models.User{}).Where("uuid = ?", user.UUID).
			Update("two_factor", encrypted).Error; err != nil {
			return err
		}
	}
	var sessions []models.Session
	if err := db.Find(&sessions).Error; err != nil {
		return err
	}
	for _, session := range sessions {
		if looksHashedSession(session.Session) {
			continue
		}
		hashed, err := hashSessionToken(session.Session)
		if err != nil {
			return err
		}
		if err := db.Model(&models.Session{}).Where("session = ?", session.Session).
			Update("session", hashed).Error; err != nil {
			return err
		}
	}
	return nil
}

func InvalidateAllSessions() error {
	db := dbcore.GetDBInstance()
	return db.Where("1 = 1").Delete(&models.Session{}).Error
}
