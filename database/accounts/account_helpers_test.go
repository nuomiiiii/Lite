package accounts

import (
	"crypto/subtle"
	"strings"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
)

func CreateAccount(username, passwd string) (models.User, error) {
	return CreateAccountWithDB(dbcore.GetDBInstance(), username, passwd)
}

func DeleteAccountByUsername(username string) error {
	return DeleteAccountByUsernameWithDB(dbcore.GetDBInstance(), username)
}

func verifyPasswd(passwd, encoded string) bool {
	if strings.HasPrefix(encoded, argonPrefix) {
		return verifyArgon2id(passwd, encoded)
	}
	legacy := hashLegacySHA256(passwd)
	return subtle.ConstantTimeCompare([]byte(legacy), []byte(encoded)) == 1
}
