package notification

import (
	"fmt"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/notifier"
	"gorm.io/gorm"
)

func AddLoadNotification(clients []string, defaultOn bool, name string, metric string, threshold float32, ratio float32, interval int) (uint, error) {
	db := dbcore.GetDBInstance()
	notification := models.LoadNotification{
		Clients:   normalizeLoadNotificationClients(clients),
		DefaultOn: defaultOn,
		Name:      name,
		Metric:    metric,
		Threshold: threshold,
		Ratio:     ratio,
		Interval:  interval,
	}
	if err := db.Create(&notification).Error; err != nil {
		return 0, err
	}

	return notification.Id, ReloadLoadNotificationSchedule()
}
func DeleteLoadNotification(id []uint) error {
	db := dbcore.GetDBInstance()
	result := db.Where("id IN ?", id).Delete(&models.LoadNotification{})
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return ReloadLoadNotificationSchedule()
}

func EditLoadNotification(notifications []*models.LoadNotification) error {
	if err := editLoadNotifications(dbcore.GetDBInstance(), notifications); err != nil {
		return err
	}
	return ReloadLoadNotificationSchedule()
}

func editLoadNotifications(db *gorm.DB, notifications []*models.LoadNotification) error {
	if len(notifications) == 0 {
		return fmt.Errorf("at least one load notification is required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, notification := range notifications {
			if notification == nil || notification.Id == 0 {
				return fmt.Errorf("load notification ID is required")
			}
			clients := normalizeLoadNotificationClients(notification.Clients)
			if !notification.DefaultOn && len(clients) == 0 {
				return fmt.Errorf("clients is required when default_on is false")
			}
			var existing models.LoadNotification
			if err := tx.Select("id").Where("id = ?", notification.Id).First(&existing).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.LoadNotification{}).Where("id = ?", notification.Id).Updates(map[string]any{
				"name":        notification.Name,
				"clients":     clients,
				"all_clients": notification.DefaultOn,
				"metric":      notification.Metric,
				"threshold":   notification.Threshold,
				"ratio":       notification.Ratio,
				"interval":    notification.Interval,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func GetAllLoadNotifications() ([]models.LoadNotification, error) {
	db := dbcore.GetDBInstance()
	var notifications []models.LoadNotification
	if err := db.Find(&notifications).Error; err != nil {
		return nil, err
	}
	return notifications, nil
}

func SaveLoadNotification(record models.LoadNotification) error {
	db := dbcore.GetDBInstance()
	return db.Create(&record).Error
}

func ReloadLoadNotificationSchedule() error {
	db := dbcore.GetDBInstance()
	var loadNotifications []models.LoadNotification
	if err := db.Find(&loadNotifications).Error; err != nil {
		return err
	}
	return notifier.ReloadLoadNotificationSchedule(loadNotifications)
}

// AddDefaultOnClientUUID adds a newly created server to every load rule that
// opted into default-on behavior. Existing assignments are kept unchanged.
func AddDefaultOnClientUUID(uuid string) error {
	changed, err := addDefaultOnClientUUID(dbcore.GetDBInstance(), uuid)
	if err != nil || !changed {
		return err
	}
	return ReloadLoadNotificationSchedule()
}

func addDefaultOnClientUUID(db *gorm.DB, uuid string) (bool, error) {
	if uuid == "" {
		return false, nil
	}
	changed := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var notifications []models.LoadNotification
		if err := tx.Where("all_clients = ?", true).Find(&notifications).Error; err != nil {
			return err
		}
		for _, notification := range notifications {
			if containsLoadNotificationClient(notification.Clients, uuid) {
				continue
			}
			clients := append(models.StringArray{}, notification.Clients...)
			clients = append(clients, uuid)
			if err := tx.Model(&models.LoadNotification{}).Where("id = ?", notification.Id).Update("clients", clients).Error; err != nil {
				return err
			}
			changed = true
		}
		return nil
	})
	return changed, err
}

func normalizeLoadNotificationClients(clients []string) models.StringArray {
	if clients == nil {
		return models.StringArray{}
	}
	return models.StringArray(clients)
}

func containsLoadNotificationClient(clients models.StringArray, uuid string) bool {
	for _, client := range clients {
		if client == uuid {
			return true
		}
	}
	return false
}
