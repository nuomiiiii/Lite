package accounts

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

// OnUserSecurityChanged closes remote grants and sessions after password or
// 2FA changes. remotectl registers this to avoid an import cycle.
var OnUserSecurityChanged func(userUUID string)

var allowedPreferenceLanguages = map[string]struct{}{
	"en-US": {},
	"zh-CN": {},
	"zh-TW": {},
	"ja-JP": {},
	"id-ID": {},
}

var allowedPreferenceColors = map[string]struct{}{
	"gray": {}, "gold": {}, "bronze": {}, "brown": {}, "yellow": {}, "amber": {},
	"orange": {}, "tomato": {}, "red": {}, "ruby": {}, "crimson": {}, "pink": {},
	"plum": {}, "purple": {}, "violet": {}, "iris": {}, "indigo": {}, "blue": {},
	"cyan": {}, "teal": {}, "jade": {}, "green": {}, "grass": {}, "lime": {},
	"mint": {}, "sky": {},
}

// CheckPassword 检查密码是否正确
//
// 如果密码正确，返回用户的 UUID 和 true；否则返回空字符串和 false。
// 登录接口请使用 AuthenticatePassword，以便区分密码错误与系统繁忙。
func CheckPassword(username, passwd string) (uuid string, success bool) {
	uuid, err := AuthenticatePassword(username, passwd, "")
	return uuid, err == nil
}

func AuthenticatePassword(username, passwd, clientIP string) (uuid string, err error) {
	if loginThrottled(clientIP, username) {
		return "", ErrPasswordBusy
	}
	db := dbcore.GetDBInstance()
	var user models.User
	result := db.Where("username = ?", username).First(&user)
	if result.Error != nil {
		recordLoginFailure(clientIP, username)
		return "", ErrPasswordInvalid
	}
	ok, verifyErr := verifyPasswordLimited(passwd, user.Passwd)
	if verifyErr != nil {
		return "", verifyErr
	}
	if !ok {
		recordLoginFailure(clientIP, username)
		return "", ErrPasswordInvalid
	}
	maybeUpgradeLegacyPassword(user.UUID, passwd, user.Passwd)
	return user.UUID, nil
}

func maybeUpgradeLegacyPassword(uuid, passwd, encoded string) {
	if !isLegacyPasswordHash(encoded) {
		return
	}
	hashed, err := hashPasswd(passwd)
	if err != nil {
		return
	}
	_ = dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", uuid).Update("passwd", hashed).Error
}

func VerifyPasswordForUUID(uuid, passwd string) error {
	if uuid == "" || passwd == "" {
		return ErrPasswordInvalid
	}
	user, err := GetUserByUUID(uuid)
	if err != nil || user.Passwd == "" {
		return ErrPasswordInvalid
	}
	ok, verifyErr := verifyPasswordLimited(passwd, user.Passwd)
	if verifyErr != nil {
		return verifyErr
	}
	if !ok {
		return ErrPasswordInvalid
	}
	maybeUpgradeLegacyPassword(uuid, passwd, user.Passwd)
	return nil
}

// ForceResetPassword 强制重置用户密码
func ForceResetPassword(username, passwd string) (err error) {
	hashed, err := hashPasswd(passwd)
	if err != nil {
		return err
	}
	db := dbcore.GetDBInstance()
	result := db.Model(&models.User{}).Where("username = ?", username).Update("passwd", hashed)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("无法找到用户名")
	}
	return nil
}

// hashPasswd 对密码进行加盐哈希
func CreateAccount(username, passwd string) (user models.User, err error) {
	return CreateAccountWithDB(dbcore.GetDBInstance(), username, passwd)
}

func CreateAccountWithDB(db *gorm.DB, username, passwd string) (user models.User, err error) {
	hashedPassword, err := hashPasswd(passwd)
	if err != nil {
		return models.User{}, err
	}
	user = models.User{
		UUID:     uuid.New().String(),
		Username: username,
		Passwd:   hashedPassword,
	}
	err = db.Create(&user).Error
	if err != nil {
		return models.User{}, err
	}
	return user, nil
}

func DeleteAccountByUsername(username string) (err error) {
	return DeleteAccountByUsernameWithDB(dbcore.GetDBInstance(), username)
}

func DeleteAccountByUsernameWithDB(db *gorm.DB, username string) (err error) {
	err = db.Where("username = ?", username).Delete(&models.User{}).Error
	if err != nil {
		return err
	}
	return nil
}

func GetUserByUUID(uuid string) (user models.User, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&user).Error
	if err != nil {
		return models.User{}, err
	}
	return user, nil
}

// 通过 SSO 信息获取用户
func GetUserBySSO(ssoID string) (user models.User, err error) {
	db := dbcore.GetDBInstance()

	// 首先尝试查找已存在的用户
	err = db.Where("sso_id = ?", ssoID).First(&user).Error
	if err == nil {
		return user, nil
	}

	// 如果找不到用户，返回明确的错误信息
	return models.User{}, fmt.Errorf("用户不存在：%s", ssoID)
}

func BindingExternalAccount(uuid string, sso_id string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Update("sso_id", sso_id).Error
	if err != nil {
		return err
	}
	return nil
}

func UnbindExternalAccount(uuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Update("sso_id", "").Error
	if err != nil {
		return err
	}
	return nil
}

func UpdateUser(uuid string, name, password, sso_type *string) error {
	db := dbcore.GetDBInstance()
	// Check if user exists
	var existingUser models.User
	result := db.Where("uuid = ?", uuid).First(&existingUser)
	if result.Error != nil {
		return fmt.Errorf("user not found: %s", uuid)
	}
	updates := make(map[string]interface{})
	if name != nil {
		updates["username"] = *name
	}
	if password != nil {
		hashed, hashErr := hashPasswd(*password)
		if hashErr != nil {
			return hashErr
		}
		updates["passwd"] = hashed
	}
	if sso_type != nil {
		updates["sso_type"] = *sso_type
	}
	updates["updated_at"] = time.Now().UTC()
	err := db.Model(&models.User{}).Where("uuid = ?", uuid).Updates(updates).Error
	if err != nil {
		return err
	}
	if password != nil {
		DeleteAllSessions()
		if OnUserSecurityChanged != nil {
			OnUserSecurityChanged(uuid)
		}
	}
	return nil
}

// UpdateUserPreferences updates only the UI preferences owned by one account.
func UpdateUserPreferences(uuid string, language, color *string) error {
	return UpdateUserPreferencesWithDB(dbcore.GetDBInstance(), uuid, language, color)
}

func UpdateUserPreferencesWithDB(db *gorm.DB, uuid string, language, color *string) error {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return fmt.Errorf("user UUID is required")
	}

	updates := make(map[string]interface{}, 3)
	if language != nil {
		normalized := strings.TrimSpace(*language)
		if _, ok := allowedPreferenceLanguages[normalized]; !ok {
			return fmt.Errorf("unsupported language preference")
		}
		updates["language"] = normalized
	}
	if color != nil {
		normalized := strings.TrimSpace(*color)
		if _, ok := allowedPreferenceColors[normalized]; !ok {
			return fmt.Errorf("unsupported color preference")
		}
		updates["color"] = normalized
	}
	if len(updates) == 0 {
		return fmt.Errorf("at least one preference is required")
	}
	updates["updated_at"] = time.Now().UTC()

	result := db.Model(&models.User{}).Where("uuid = ?", uuid).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("user not found: %s", uuid)
	}
	return nil
}
