package accounts

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/akizon77/komari/database/dbcore"
	"github.com/akizon77/komari/database/models"
	"github.com/akizon77/komari/utils"

	"github.com/google/uuid"
)

const constantSalt = "06Wm4Jv1Hkxx"

// CheckPassword 检查密码是否正确
//
// 如果密码正确，返回用户的 UUID 和 true；否则返回空字符串和 false
func CheckPassword(username, passwd string) (uuid string, success bool) {
	db := dbcore.GetDBInstance()
	var user models.User
	err := db.Where("username = ?", username).First(&user).Error
	if err != nil {
		return "", false
	}
	if hashPasswd(passwd) != user.Passwd {
		return "", false
	}
	return user.UUID, true
}

// ForceResetPassword 强制重置用户密码
func ForceResetPassword(username, passwd string) (err error) {
	db := dbcore.GetDBInstance()
	result := db.Model(&models.User{}).Where("username = ?", username).Update("passwd", hashPasswd(passwd))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("无法找到用户名")
	}
	return nil
}

// hashPasswd 对密码进行加盐哈希
func hashPasswd(passwd string) string {
	saltedPassword := passwd + constantSalt
	hash := sha256.New()
	hash.Write([]byte(saltedPassword))
	hashedPassword := base64.StdEncoding.EncodeToString(hash.Sum(nil))
	return hashedPassword
}

// 创建默认管理员账户，使用环境变量 ADMIN_USERNAME 作为用户名，环境变量 ADMIN_PASSWORD 作为密码
func CreateDefaultAdminAccount() (username, passwd string, err error) {
	db := dbcore.GetDBInstance()

	username = os.Getenv("ADMIN_USERNAME")
	if username == "" {
		username = "admin"
	}

	passwd = os.Getenv("ADMIN_PASSWORD")
	if passwd == "" {
		passwd = utils.GeneratePassword()
	}

	hashedPassword := hashPasswd(passwd)

	user := models.User{
		UUID:     uuid.New().String(),
		Username: username,
		Passwd:   hashedPassword,
	}

	err = db.Create(&user).Error
	if err != nil {
		return "", "", err
	}

	return username, passwd, nil
}

func GetUserByUUID(uuid string) (user models.User, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&user).Error
	if err != nil {
		return models.User{}, err
	}
	return user, nil
}

// GetOrCreateUserBySSO 通过 SSO 信息获取或创建用户
func GetOrCreateUserBySSO(ssoType, ssoID, username string) (user models.User, err error) {
	db := dbcore.GetDBInstance()

	// 首先尝试查找已存在的用户
	err = db.Where("sso_type = ? AND sso_id = ?", ssoType, ssoID).First(&user).Error
	if err == nil {
		return user, nil
	}

	// 如果用户不存在，创建新用户
	user = models.User{
		UUID:     uuid.New().String(),
		Username: username,
		SSOType:  ssoType,
		SSOID:    ssoID,
	}

	err = db.Create(&user).Error
	if err != nil {
		return models.User{}, err
	}

	return user, nil
}
