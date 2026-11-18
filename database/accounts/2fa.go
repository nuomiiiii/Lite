package accounts

import (
	"errors"
	"image"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/pquerna/otp/totp"
)

var (
	TwoFactorIssuer            = "轻量.Lite"
	ErrTwoFactorInvalid        = errors.New("Invalid 2FA code")
	ErrTwoFactorClosed         = errors.New("2FA is unavailable")
	ErrTwoFactorAlreadyEnabled = errors.New("2FA is already enabled; disable it first")
)

func Generate2Fa() (string, image.Image, error) {
	otpKey, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TwoFactorIssuer,
		AccountName: "lite",
	})
	if err != nil {
		return "", nil, err
	}
	img, err := otpKey.Image(250, 250)
	if err != nil {
		return "", nil, err
	}
	return otpKey.Secret(), img, nil
}

func Enable2Fa(uuid, secret, code string) error {
	if secret == "" || code == "" {
		return ErrTwoFactorInvalid
	}
	db := dbcore.GetDBInstance()
	var user models.User
	if err := db.Where("uuid = ?", uuid).First(&user).Error; err != nil {
		return err
	}
	if user.TwoFactor != "" {
		return ErrTwoFactorAlreadyEnabled
	}
	counter, ok := matchingTOTPCounter(secret, code, 0, time.Now())
	if !ok {
		return ErrTwoFactorInvalid
	}
	encrypted, err := encryptTOTPSecret(secret)
	if err != nil {
		return err
	}
	res := db.Model(&models.User{}).Where("uuid = ? AND (two_factor = '' OR two_factor IS NULL)", uuid).Updates(map[string]interface{}{
		"two_factor":         encrypted,
		"two_factor_counter": counter,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTwoFactorAlreadyEnabled
	}
	notifyUserSecurityChanged(uuid)
	return nil
}

func Verify2Fa(uuid, code string) (bool, error) {
	db := dbcore.GetDBInstance()
	var user models.User
	err := db.Where("uuid = ?", uuid).First(&user).Error
	if err != nil {
		return false, err
	}
	if user.TwoFactor == "" {
		return false, nil
	}
	secret, err := decryptTOTPSecret(user.TwoFactor)
	if err != nil {
		return false, ErrTwoFactorClosed
	}
	ok, err := consumeTOTPCounter(uuid, code, secret, user.TwoFactorCounter)
	if err != nil {
		return false, ErrTwoFactorClosed
	}
	return ok, nil
}

func Disable2Fa(uuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Updates(map[string]interface{}{
		"two_factor":         "",
		"two_factor_counter": 0,
	}).Error
	if err != nil {
		return err
	}
	notifyUserSecurityChanged(uuid)
	return nil
}

func DeleteOtherSessions(userUUID, keepSession string) error {
	if userUUID == "" {
		return nil
	}
	db := dbcore.GetDBInstance()
	var err error
	if keepSession == "" {
		err = db.Where("uuid = ?", userUUID).Delete(&models.Session{}).Error
	} else {
		hashed, hashErr := hashSessionToken(keepSession)
		q := db.Where("uuid = ? AND session <> ?", userUUID, keepSession)
		if hashErr == nil && hashed != keepSession {
			q = db.Where("uuid = ? AND session <> ? AND session <> ?", userUUID, hashed, keepSession)
		}
		err = q.Delete(&models.Session{}).Error
	}
	if err != nil {
		return err
	}
	notifyUserSecurityChanged(userUUID)
	return nil
}
