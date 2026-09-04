package accounts

import (
	"strings"

	"github.com/nuomiiiii/lite/database/dbcore"
)

func hasEncryptedTOTPSecrets() (bool, error) {
	db := dbcore.GetDBInstance()
	var users []struct {
		TwoFactor string
	}
	if err := db.Table("users").Select("two_factor").Find(&users).Error; err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "no such table") || strings.Contains(msg, "does not exist") {
			return false, nil
		}
		return false, err
	}
	for _, user := range users {
		if strings.HasPrefix(user.TwoFactor, totpSecretPrefix) {
			return true, nil
		}
	}
	return false, nil
}
