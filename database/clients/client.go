package clients

import (
	"time"

	"github.com/akizon77/komari/common"
	"github.com/akizon77/komari/database/dbcore"
	"github.com/akizon77/komari/database/models"
	"github.com/akizon77/komari/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 删除指定 UUID 的客户端配置
func DeleteClientConfig(clientUuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Delete(&common.ClientConfig{ClientUUID: clientUuid}).Error
	if err != nil {
		return err
	}
	return nil
}

// 更新或插入客户端基本信息
func UpdateOrInsertBasicInfo(cbi ClientBasicInfo) error {
	db := dbcore.GetDBInstance()
	err := db.Save(&cbi).Error
	if err != nil {
		return err
	}
	return nil
}

// 更新客户端设置
func UpdateClientConfig(config common.ClientConfig) error {
	db := dbcore.GetDBInstance()
	err := db.Save(&config).Error
	if err != nil {
		return err
	}
	return nil
}

func EditClientName(clientUUID, clientName string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Update("client_name", clientName).Error
	if err != nil {
		return err
	}
	return nil
}

// UpdateClientByUUID 更新指定 UUID 的客户端配置
func UpdateClientByUUID(config common.ClientConfig) error {
	db := dbcore.GetDBInstance()
	result := db.Model(&common.ClientConfig{}).Where("client_uuid = ?", config.ClientUUID).Updates(config)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func EditClientToken(clientUUID, token string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Update("token", token).Error
	if err != nil {
		return err
	}
	return nil
}

// CreateClient 创建新客户端
func CreateClient(config common.ClientConfig) (clientUUID, token string, err error) {
	db := dbcore.GetDBInstance()
	token = utils.GenerateToken()
	clientUUID = uuid.New().String()

	client := models.Client{
		UUID:      clientUUID,
		Token:     token,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	config.ClientUUID = clientUUID

	err = db.Create(&client).Error
	if err != nil {
		return "", "", err
	}
	err = db.Create(&config).Error
	if err != nil {
		return "", "", err
	}
	return clientUUID, token, nil
}

// GetAllClients 获取所有客户端配置
func GetAllClients() (clients []models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Find(&clients).Error
	if err != nil {
		return nil, err
	}
	return clients, nil
}

func GetClientByUUID(uuid string) (client models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		return models.Client{}, err
	}
	return client, nil
}

// GetClientConfig 获取指定 UUID 的客户端配置
func GetClientConfig(uuid string) (client common.ClientConfig, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("client_uuid = ?", uuid).First(&client).Error
	if err != nil {
		return common.ClientConfig{}, err
	}
	return client, nil
}

// ClientBasicInfo 客户端基本信息（假设的结构体，需根据实际定义调整）
type ClientBasicInfo struct {
	CPU       common.CPUReport `json:"cpu"`
	GPU       common.GPUReport `json:"gpu"`
	IpAddress common.IPAddress `json:"ip"`
	OS        string           `json:"os"`
}

// GetClientBasicInfo 获取指定 UUID 的客户端基本信息
func GetClientBasicInfo(uuid string) (client ClientBasicInfo, err error) {
	db := dbcore.GetDBInstance()
	var clientInfo common.ClientInfo
	err = db.Where("client_uuid = ?", uuid).First(&clientInfo).Error
	if err != nil {
		return client, err
	}

	client = ClientBasicInfo{
		CPU: common.CPUReport{
			Name:  clientInfo.CPUNAME,
			Arch:  clientInfo.CPUARCH,
			Cores: clientInfo.CPUCORES,
		},
		GPU: common.GPUReport{
			Name: clientInfo.GPUNAME,
		},
		OS: clientInfo.OS,
		// IpAddress: 未在数据库中找到对应字段，需确认
	}

	return client, nil
}
