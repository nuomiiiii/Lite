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
	TwoFactorIssuer     = "Lite"
	ErrTwoFactorInvalid = errors.New("Invalid 2FA code")
	ErrTwoFactorClosed  = errors.New("2FA is unavailable")
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
	counter, ok := matchingTOTPCounter(secret, code, 0, time.Now())
	if !ok {
		return ErrTwoFactorInvalid
	}
	encrypted, err := encryptTOTPSecret(secret)
	if err != nil {
		return err
	}
	db := dbcore.GetDBInstance()
	if err := db.Model(&models.User{}).Where("uuid = ?", uuid).Updates(map[string]interface{}{
		"two_factor":         encrypted,
		"two_factor_counter": counter,
	}).Error; err != nil {
		return err
	}
	if OnUserSecurityChanged != nil {
		OnUserSecurityChanged(uuid)
	}
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
	if OnUserSecurityChanged != nil {
		OnUserSecurityChanged(uuid)
	}
	return nil
}
