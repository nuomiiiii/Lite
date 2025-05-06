package clients

import (
	"time"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
	"gorm.io/gorm/clause"

	"github.com/google/uuid"
)

// Deprecated: DeleteClientConfig is deprecated and will be removed in a future release. Use DeleteClient instead.
func DeleteClientConfig(clientUuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Delete(&common.ClientConfig{ClientUUID: clientUuid}).Error
	if err != nil {
		return err
	}
	return nil
}
func DeleteClient(clientUuid string) error {
	db := dbcore.GetDBInstance()
	err := db.Delete(&models.Client{}, "uuid = ?", clientUuid).Error
	if err != nil {
		return err
	}
	err = db.Delete(&common.ClientInfo{}, "uuid = ?", clientUuid).Error
	if err != nil {
		return err
	}
	return nil
}

// 更新或插入客户端基本信息
func UpdateOrInsertBasicInfo(cbi common.ClientInfo) error {
	db := dbcore.GetDBInstance()
	updates := make(map[string]interface{})

	if cbi.Name != "" {
		updates["name"] = cbi.Name
	}
	if cbi.CpuName != "" {
		updates["cpu_name"] = cbi.CpuName
	}
	if cbi.Arch != "" {
		updates["arch"] = cbi.Arch
	}
	if cbi.CpuCores != 0 {
		updates["cpu_cores"] = cbi.CpuCores
	}
	if cbi.OS != "" {
		updates["os"] = cbi.OS
	}
	if cbi.GpuName != "" {
		updates["gpu_name"] = cbi.GpuName
	}
	if cbi.IPv4 != "" {
		updates["ipv4"] = cbi.IPv4
	}
	if cbi.IPv6 != "" {
		updates["ipv6"] = cbi.IPv6
	}
	if cbi.Region != "" {
		updates["region"] = cbi.Region
	}
	if cbi.Remark != "" {
		updates["remark"] = cbi.Remark
	}
	updates["mem_total"] = cbi.MemTotal
	updates["swap_total"] = cbi.SwapTotal
	updates["disk_total"] = cbi.DiskTotal
	updates["version"] = cbi.Version
	updates["updated_at"] = time.Now()

	err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "uuid"}},
		DoUpdates: clause.Assignments(updates),
	}).Create(&cbi).Error
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

/*
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
*/
func EditClientToken(clientUUID, token string) error {
	db := dbcore.GetDBInstance()
	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Update("token", token).Error
	if err != nil {
		return err
	}
	return nil
}

// CreateClient 创建新客户端
func CreateClient() (clientUUID, token string, err error) {
	db := dbcore.GetDBInstance()
	token = utils.GenerateToken()
	clientUUID = uuid.New().String()

	client := models.Client{
		UUID:      clientUUID,
		Token:     token,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err = db.Create(&client).Error
	if err != nil {
		return "", "", err
	}
	clientInfo := common.ClientInfo{
		UUID: clientUUID,
		Name: "client_" + clientUUID[0:8],
	}
	err = db.Create(&clientInfo).Error
	if err != nil {
		return "", "", err
	}
	return clientUUID, token, nil
}

/*
// GetAllClients 获取所有客户端配置

	func getAllClients() (clients []models.Client, err error) {
		db := dbcore.GetDBInstance()
		err = db.Find(&clients).Error
		if err != nil {
			return nil, err
		}
		return clients, nil
	}
*/
func GetClientByUUID(uuid string) (client models.Client, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		return models.Client{}, err
	}
	return client, nil
}

// GetClientBasicInfo 获取指定 UUID 的客户端基本信息
func GetClientBasicInfo(uuid string) (client common.ClientInfo, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		return client, err
	}

	return client, nil
}

func GetClientTokenByUUID(uuid string) (token string, err error) {
	db := dbcore.GetDBInstance()
	var client models.Client
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		return "", err
	}
	return client.Token, nil
}

func GetAllClientBasicInfo() (clients []common.ClientInfo, err error) {
	db := dbcore.GetDBInstance()
	err = db.Find(&clients).Error
	if err != nil {
		return nil, err
	}
	return clients, nil
}
