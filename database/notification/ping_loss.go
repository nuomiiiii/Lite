package notification

import (
	"errors"
	"fmt"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

func ValidatePingLossNotification(notification models.PingLossNotification) error {
	if notification.Client == "" {
		return fmt.Errorf("client UUID cannot be empty")
	}
	if notification.TaskId == 0 {
		return fmt.Errorf("ping task is required")
	}
	if notification.WindowSeconds < 60 || notification.WindowSeconds > 24*60*60 {
		return fmt.Errorf("window must be between 60 and 86400 seconds")
	}
	if notification.LossThreshold <= 0 || notification.LossThreshold > 100 {
		return fmt.Errorf("loss threshold must be greater than 0 and at most 100")
	}
	if notification.MinimumSamples < 1 || notification.MinimumSamples > 100000 {
		return fmt.Errorf("minimum samples must be between 1 and 100000")
	}
	if notification.CooldownSeconds < 60 || notification.CooldownSeconds > 7*24*60*60 {
		return fmt.Errorf("cooldown must be between 60 and 604800 seconds")
	}
	return nil
}

func validatePingLossTarget(db *gorm.DB, notification models.PingLossNotification) error {
	var client models.Client
	if err := db.Select("uuid").Where("uuid = ?", notification.Client).First(&client).Error; err != nil {
		return fmt.Errorf("client does not exist: %w", err)
	}

	var task models.PingTask
	if err := db.Where("id = ?", notification.TaskId).First(&task).Error; err != nil {
		return fmt.Errorf("ping task does not exist: %w", err)
	}
	if !task.AppliesToClient(notification.Client) {
		return fmt.Errorf("ping task is not assigned to the selected client")
	}
	return nil
}

func AddPingLossNotification(notification models.PingLossNotification) (uint, error) {
	if err := ValidatePingLossNotification(notification); err != nil {
		return 0, err
	}
	db := dbcore.GetDBInstance()
	if err := validatePingLossTarget(db, notification); err != nil {
		return 0, err
	}
	notification.Id = 0
	notification.LastNotified = nil
	notification.AlertActive = false
	if err := db.Create(&notification).Error; err != nil {
		return 0, err
	}
	return notification.Id, nil
}

func pingLossNotificationUpdates(notification *models.PingLossNotification) map[string]any {
	return map[string]any{
		"client":           notification.Client,
		"task_id":          notification.TaskId,
		"enable":           notification.Enable,
		"window_seconds":   notification.WindowSeconds,
		"loss_threshold":   notification.LossThreshold,
		"minimum_samples":  notification.MinimumSamples,
		"cooldown_seconds": notification.CooldownSeconds,
	}
}

func EditPingLossNotifications(notifications []*models.PingLossNotification) error {
	if len(notifications) == 0 {
		return fmt.Errorf("at least one notification is required")
	}
	db := dbcore.GetDBInstance()
	return db.Transaction(func(tx *gorm.DB) error {
		for _, notification := range notifications {
			if notification == nil || notification.Id == 0 {
				return fmt.Errorf("notification ID is required")
			}
			if err := ValidatePingLossNotification(*notification); err != nil {
				return err
			}
			if err := validatePingLossTarget(tx, *notification); err != nil {
				return err
			}
			result := tx.Model(&models.PingLossNotification{}).Where("id = ?", notification.Id).Updates(pingLossNotificationUpdates(notification))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
		}
		return nil
	})
}

// UpsertPingLossNotifications applies one or more target-specific alert
// configurations atomically. Existing targets keep their notification history.
func UpsertPingLossNotifications(notifications []*models.PingLossNotification) error {
	return upsertPingLossNotifications(dbcore.GetDBInstance(), notifications)
}

func upsertPingLossNotifications(db *gorm.DB, notifications []*models.PingLossNotification) error {
	if len(notifications) == 0 {
		return fmt.Errorf("at least one notification is required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, notification := range notifications {
			if notification == nil {
				return fmt.Errorf("notification cannot be null")
			}
			if err := ValidatePingLossNotification(*notification); err != nil {
				return err
			}
			if err := validatePingLossTarget(tx, *notification); err != nil {
				return err
			}

			var existing models.PingLossNotification
			err := tx.Select("id").Where(
				"client = ? AND task_id = ?",
				notification.Client,
				notification.TaskId,
			).First(&existing).Error
			switch {
			case err == nil:
				if err := tx.Model(&models.PingLossNotification{}).
					Where("id = ?", existing.Id).
					Updates(pingLossNotificationUpdates(notification)).Error; err != nil {
					return err
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				candidate := *notification
				candidate.Id = 0
				candidate.LastNotified = nil
				candidate.AlertActive = false
				if err := tx.Create(&candidate).Error; err != nil {
					return err
				}
			default:
				return err
			}
		}
		return nil
	})
}

func DeletePingLossNotifications(ids []uint) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one notification ID is required")
	}
	db := dbcore.GetDBInstance()
	result := db.Where("id IN ?", ids).Delete(&models.PingLossNotification{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func ListPingLossNotifications() ([]models.PingLossNotification, error) {
	db := dbcore.GetDBInstance()
	var notifications []models.PingLossNotification
	err := db.Preload("ClientInfo").Preload("Task").Order("id DESC").Find(&notifications).Error
	return notifications, err
}
