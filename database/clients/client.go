package clients

import (
	"encoding/json"
	"fmt"
	logger "github.com/komari-monitor/komari/utils/log"
	"math"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func DeleteClient(clientUuid string) error {
	db := dbcore.GetDBInstance()
	pingTasksChanged, err := deleteClient(db, clientUuid)
	if err != nil {
		return err
	}
	if pingTasksChanged {
		return tasks.ReloadPingSchedule()
	}
	return nil
}

func deleteClient(db *gorm.DB, clientUuid string) (bool, error) {
	pingTasksChanged := false
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("client = ?", clientUuid).Delete(&models.PingLossNotification{}).Error; err != nil {
			return fmt.Errorf("delete client ping loss notifications: %w", err)
		}

		var pingTasks []models.PingTask
		if err := tx.Select("id", "clients").Find(&pingTasks).Error; err != nil {
			return fmt.Errorf("find client ping tasks: %w", err)
		}
		for _, task := range pingTasks {
			clients := make(models.StringArray, 0, len(task.Clients))
			changed := false
			for _, assignedClient := range task.Clients {
				if assignedClient == clientUuid {
					changed = true
					continue
				}
				clients = append(clients, assignedClient)
			}
			if !changed {
				continue
			}
			if err := tx.Model(&models.PingTask{}).Where("id = ?", task.Id).Update("clients", clients).Error; err != nil {
				return fmt.Errorf("remove client from ping task %d: %w", task.Id, err)
			}
			pingTasksChanged = true
		}

		if err := tx.Delete(&models.Client{}, "uuid = ?", clientUuid).Error; err != nil {
			return fmt.Errorf("delete client: %w", err)
		}
		return nil
	})
	return pingTasksChanged, err
}

func SaveClientInfo(update map[string]interface{}) error {
	db := dbcore.GetDBInstance()
	clientUUID, ok := update["uuid"].(string)
	if !ok || clientUUID == "" {
		return fmt.Errorf("invalid client UUID")
	}

	// 确保更新的字段不为空
	if len(update) == 0 {
		return fmt.Errorf("no fields to update")
	}

	update["updated_at"] = time.Now().UTC()

	toFloat64 := func(value interface{}) (float64, bool) {
		switch typed := value.(type) {
		case float64:
			return typed, true
		case float32:
			return float64(typed), true
		case int:
			return float64(typed), true
		case int8:
			return float64(typed), true
		case int16:
			return float64(typed), true
		case int32:
			return float64(typed), true
		case int64:
			return float64(typed), true
		case uint:
			return float64(typed), true
		case uint8:
			return float64(typed), true
		case uint16:
			return float64(typed), true
		case uint32:
			return float64(typed), true
		case uint64:
			return float64(typed), true
		case json.Number:
			parsed, err := typed.Float64()
			if err != nil {
				return 0, false
			}
			return parsed, true
		default:
			return 0, false
		}
	}

	checkOptionalInt := func(name, key string, maxValue float64) error {
		value, exists := update[key]
		if !exists || value == nil {
			return nil
		}

		numericValue, ok := toFloat64(value)
		if !ok {
			return fmt.Errorf("%s must be a valid number", name)
		}
		if numericValue < 0 || numericValue > maxValue {
			return fmt.Errorf("%s must be a valid non-negative number: %v", name, value)
		}
		return nil
	}

	verify := func(update map[string]interface{}) error {
		if err := checkOptionalInt("Cpu.Cores", "cpu_cores", math.MaxInt-1); err != nil {
			return err
		}
		if err := checkOptionalInt("Cpu.PhysicalCores", "cpu_physical_cores", math.MaxInt-1); err != nil {
			return err
		}
		if err := checkOptionalInt("Ram.Total", "mem_total", math.MaxInt64-1); err != nil {
			return err
		}
		if err := checkOptionalInt("Swap.Total", "swap_total", math.MaxInt64-1); err != nil {
			return err
		}
		if err := checkOptionalInt("Disk.Total", "disk_total", math.MaxInt64-1); err != nil {
			return err
		}
		return nil
	}

	if err := verify(update); err != nil {
		return err
	}

	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Updates(update).Error
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
		Name:      "client_" + clientUUID[0:8],
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	err = db.Create(&client).Error
	if err != nil {
		return "", "", err
	}
	if err := tasks.AddDefaultOnClientUUID(clientUUID); err != nil {
		logger.ErrorArgs("clients", "Failed to apply default-on ping tasks to new client:", err)
	}
	return clientUUID, token, nil
}

func CreateClientWithName(name string) (clientUUID, token string, err error) {
	if name == "" {
		return CreateClient()
	}
	db := dbcore.GetDBInstance()
	token = utils.GenerateToken()
	clientUUID = uuid.New().String()
	client := models.Client{
		UUID:      clientUUID,
		Token:     token,
		Name:      name,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	err = db.Create(&client).Error
	if err != nil {
		return "", "", err
	}
	if err := tasks.AddDefaultOnClientUUID(clientUUID); err != nil {
		logger.ErrorArgs("clients", "Failed to apply default-on ping tasks to new client:", err)
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

func GetClientTokenByUUID(uuid string) (token string, err error) {
	db := dbcore.GetDBInstance()
	var client models.Client
	err = db.Where("uuid = ?", uuid).First(&client).Error
	if err != nil {
		return "", err
	}
	return client.Token, nil
}

func RotateClientToken(uuid string, gracePeriod time.Duration) (token string, previousExpiresAt time.Time, err error) {
	return rotateClientToken(dbcore.GetDBInstance(), uuid, gracePeriod)
}

func rotateClientToken(db *gorm.DB, uuid string, gracePeriod time.Duration) (token string, previousExpiresAt time.Time, err error) {
	if gracePeriod <= 0 {
		return "", time.Time{}, fmt.Errorf("token grace period must be positive")
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var client models.Client
		if err := tx.Where("uuid = ?", uuid).First(&client).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if client.PreviousToken != "" && client.PreviousTokenExpiresAt != nil && client.PreviousTokenExpiresAt.After(now) {
			return fmt.Errorf("Token 重置仍在过渡期内，请先使用新 Token 重新部署 Agent；新 Token 首次成功连接后才能再次重置")
		}
		token = utils.GenerateToken()
		previousExpiresAt = now.Add(gracePeriod)
		return tx.Model(&models.Client{}).Where("uuid = ?", uuid).Updates(map[string]interface{}{
			"token":                     token,
			"previous_token":            client.Token,
			"previous_token_expires_at": previousExpiresAt,
			"updated_at":                now,
		}).Error
	})
	return token, previousExpiresAt, err
}

func GetAllClientBasicInfo() (clients []models.Client, err error) {
	return getClientBasicInfo(dbcore.GetDBInstance())
}

func GetClientBasicInfoByUUIDs(uuids []string) (clients []models.Client, err error) {
	if len(uuids) == 0 {
		return []models.Client{}, nil
	}
	return getClientBasicInfo(dbcore.GetDBInstance().Where("uuid IN ?", uuids))
}

func getClientBasicInfo(query *gorm.DB) (clients []models.Client, err error) {
	err = query.Order("weight ASC").Order("created_at ASC").Order("uuid ASC").Find(&clients).Error
	if err != nil {
		return nil, err
	}
	return clients, nil
}

func SaveClient(updates map[string]interface{}) error {
	return saveClient(dbcore.GetDBInstance(), updates)
}

func saveClient(db *gorm.DB, updates map[string]interface{}) error {
	clientUUID, ok := updates["uuid"].(string)
	if !ok || clientUUID == "" {
		return fmt.Errorf("invalid client UUID")
	}

	// 确保更新的字段不为空
	if len(updates) == 0 {
		return fmt.Errorf("no fields to update")
	}

	if v, exists := updates["traffic_limit"]; exists {
		if val, ok := v.(float64); ok {
			if val < 0 || val > math.MaxInt64-1 {
				return fmt.Errorf("traffic_limit must be a valid non-negative int64 value, got %v", val)
			}
		}
	}
	if value, exists := updates["traffic_reset_day"]; exists {
		normalized, err := normalizeTrafficResetDay(value)
		if err != nil {
			return err
		}
		updates["traffic_reset_day"] = normalized
	}
	if value, exists := updates["currency"]; exists {
		currency, ok := value.(string)
		if !ok {
			return fmt.Errorf("currency must be a string")
		}
		currency = strings.TrimSpace(currency)
		if strings.EqualFold(currency, "CAD") || strings.EqualFold(currency, "CA$") || strings.EqualFold(currency, "C$") {
			currency = "CAD"
		}
		updates["currency"] = currency
	}
	if value, exists := updates["expired_at"]; exists {
		switch typed := value.(type) {
		case nil:
			updates["expired_at"] = nil
		case time.Time:
			updates["expired_at"] = typed.UTC()
		case *time.Time:
			if typed == nil {
				updates["expired_at"] = nil
			} else {
				updates["expired_at"] = typed.UTC()
			}
		case string:
			stamp, err := time.Parse(time.RFC3339Nano, typed)
			if err != nil {
				return fmt.Errorf("expired_at must be an RFC3339 timestamp with a timezone: %w", err)
			}
			updates["expired_at"] = stamp.UTC()
		default:
			return fmt.Errorf("expired_at must be an RFC3339 timestamp with a timezone")
		}
	}

	updates["updated_at"] = time.Now().UTC()

	err := db.Model(&models.Client{}).Where("uuid = ?", clientUUID).Updates(updates).Error
	if err != nil {
		return err
	}
	return nil
}

func normalizeTrafficResetDay(value interface{}) (*int, error) {
	if value == nil {
		return nil, nil
	}
	numericValue, ok := value.(float64)
	if !ok {
		switch typed := value.(type) {
		case int:
			numericValue = float64(typed)
		case int32:
			numericValue = float64(typed)
		case int64:
			numericValue = float64(typed)
		case json.Number:
			parsed, err := typed.Float64()
			if err != nil {
				return nil, fmt.Errorf("traffic_reset_day must be an integer from 0 to 31")
			}
			numericValue = parsed
		default:
			return nil, fmt.Errorf("traffic_reset_day must be an integer from 0 to 31")
		}
	}
	if math.Trunc(numericValue) != numericValue || numericValue < 0 || numericValue > 31 {
		return nil, fmt.Errorf("traffic_reset_day must be an integer from 0 to 31")
	}
	day := int(numericValue)
	return &day, nil
}

// AdoptTrafficResetDay records an Agent's existing setting only while the node
// has not yet been explicitly managed by Komari.
func AdoptTrafficResetDay(clientUUID string, value interface{}) error {
	day, err := normalizeTrafficResetDay(value)
	if err != nil {
		return err
	}
	if day == nil {
		return nil
	}
	db := dbcore.GetDBInstance()
	return db.Model(&models.Client{}).
		Where("uuid = ? AND traffic_reset_day IS NULL", clientUUID).
		Update("traffic_reset_day", *day).Error
}
