package clients

import (
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func EditClientName(clientUUID, clientName string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Update("client_name", clientName).Error
	if err != nil {
		return err
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

// UpdateClientByUUID 更新指定 UUID 的客户端配置
func UpdateClientByUUID(client models.Client) error {
	db := dbcore.GetDBInstance()
	result := db.Model(&models.Client{}).Where("client_uuid = ?", client.UUID).Updates(client)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// CreateClient 创建新客户端
func CreateClient(clientName string) (client models.Client, err error) {
	db := dbcore.GetDBInstance()
	token := utils.GenerateToken()
	clientUUID := uuid.New().String()

	client = models.Client{
		UUID:       clientUUID,
		Token:      token,
		ClientName: clientName,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	err = db.Create(&client).Error
	if err != nil {
		return client, err
	}
	err = db.Create(&client).Error
	if err != nil {
		return client, err
	}
	return client, nil
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

func GetClientByToken(token string) (client models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("token = ?", token).First(&client).Error
	if err != nil {
		return models.Client{}, err
	}
	return client, nil
}

func GetAllClientsWithoutToken() (clients []models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Find(&clients).Error
	if err != nil {
		return nil, err
	}
	for i := range clients {
		clients[i].Token = ""
	}
	return clients, nil
}
