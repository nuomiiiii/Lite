package notification

import (
	"fmt"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/utils/notifier"
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
	if err := deleteLoadNotifications(dbcore.GetDBInstance(), id); err != nil {
		return err
	}
	return ReloadLoadNotificationSchedule()
}

func deleteLoadNotifications(db *gorm.DB, id []uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("notification_id IN ?", id).Delete(&models.LoadNotificationState{}).Error; err != nil {
			return err
		}
		result := tx.Where("id IN ?", id).Delete(&models.LoadNotification{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
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
			if err := tx.Select("id", "clients", "metric", "threshold", "ratio", "interval", "last_notified").Where("id = ?", notification.Id).First(&existing).Error; err != nil {
				return err
			}
			semanticsChanged := models.LoadNotificationRuleFingerprint(existing) != models.LoadNotificationRuleFingerprint(*notification)
			updates := map[string]any{
				"name":        notification.Name,
				"clients":     clients,
				"all_clients": notification.DefaultOn,
				"metric":      notification.Metric,
				"threshold":   notification.Threshold,
				"ratio":       notification.Ratio,
				"interval":    notification.Interval,
			}
			if semanticsChanged {
				updates["last_notified"] = nil
			}
			if err := tx.Model(&models.LoadNotification{}).Where("id = ?", notification.Id).Updates(updates).Error; err != nil {
				return err
			}
			if semanticsChanged {
				if err := tx.Where("notification_id = ?", notification.Id).Delete(&models.LoadNotificationState{}).Error; err != nil {
					return err
				}
			} else if err := deleteUnassignedLoadNotificationStates(tx, notification.Id, clients); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteUnassignedLoadNotificationStates(db *gorm.DB, notificationID uint, clients models.StringArray) error {
	query := db.Where("notification_id = ?", notificationID)
	if len(clients) > 0 {
		query = query.Where("client NOT IN ?", []string(clients))
	}
	return query.Delete(&models.LoadNotificationState{}).Error
}

type CurrentLoadAlert struct {
	NotificationID   uint       `json:"notification_id"`
	NotificationName string     `json:"notification_name"`
	Client           string     `json:"client"`
	ClientName       string     `json:"client_name"`
	Metric           string     `json:"metric"`
	Threshold        float32    `json:"threshold"`
	Ratio            float32    `json:"ratio"`
	Interval         int        `json:"interval"`
	ActiveSince      *time.Time `json:"active_since"`
	LastEvaluatedAt  time.Time  `json:"last_evaluated_at"`
	LatestValue      float64    `json:"latest_value"`
	MatchedSamples   int        `json:"matched_samples"`
	TotalSamples     int        `json:"total_samples"`
	Silenced         bool       `json:"silenced"`
	SilencedUntil    *time.Time `json:"silenced_until"`
	SilencedForever  bool       `json:"silenced_forever"`
}

func ListCurrentLoadAlerts(now time.Time) ([]CurrentLoadAlert, error) {
	return listCurrentLoadAlerts(dbcore.GetDBInstance(), now)
}

func listCurrentLoadAlerts(db *gorm.DB, now time.Time) ([]CurrentLoadAlert, error) {
	var states []models.LoadNotificationState
	if err := db.Preload("Notification").Preload("ClientInfo").
		Where("alert_active = ?", true).
		Order("active_since DESC").Order("notification_id ASC").Order("client ASC").
		Find(&states).Error; err != nil {
		return nil, err
	}
	alerts := make([]CurrentLoadAlert, 0, len(states))
	for _, state := range states {
		if !loadAlertStateCurrent(state, now) {
			continue
		}
		silencedUntil := state.SilencedUntil
		silenced := state.SilencedForever || (silencedUntil != nil && silencedUntil.After(now))
		if !silenced && !state.SilencedForever {
			silencedUntil = nil
		}
		alerts = append(alerts, CurrentLoadAlert{
			NotificationID: state.NotificationID, NotificationName: state.Notification.Name,
			Client: state.Client, ClientName: state.ClientInfo.Name,
			Metric: state.Notification.Metric, Threshold: state.Notification.Threshold,
			Ratio: state.Notification.Ratio, Interval: state.Notification.Interval,
			ActiveSince: state.ActiveSince, LastEvaluatedAt: state.LastEvaluatedAt,
			LatestValue: state.LatestValue, MatchedSamples: state.MatchedSamples,
			TotalSamples: state.TotalSamples, Silenced: silenced,
			SilencedUntil: silencedUntil, SilencedForever: state.SilencedForever,
		})
	}
	return alerts, nil
}

func SetLoadAlertSilence(notificationID uint, client, mode string, now time.Time) error {
	return setLoadAlertSilence(dbcore.GetDBInstance(), notificationID, client, mode, now)
}

func setLoadAlertSilence(db *gorm.DB, notificationID uint, client, mode string, now time.Time) error {
	client = strings.TrimSpace(client)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if notificationID == 0 || client == "" {
		return fmt.Errorf("notification_id and client are required")
	}
	var state models.LoadNotificationState
	if err := db.Preload("Notification").Where("notification_id = ? AND client = ?", notificationID, client).First(&state).Error; err != nil {
		return err
	}
	if !loadAlertStateCurrent(state, now) {
		return gorm.ErrRecordNotFound
	}
	updates := map[string]any{"silenced_until": nil, "silenced_forever": false}
	switch mode {
	case "off":
	case "24h":
		until := now.UTC().Add(24 * time.Hour)
		updates["silenced_until"] = until
	case "3d":
		until := now.UTC().Add(3 * 24 * time.Hour)
		updates["silenced_until"] = until
	case "7d":
		until := now.UTC().Add(7 * 24 * time.Hour)
		updates["silenced_until"] = until
	case "forever":
		updates["silenced_forever"] = true
	default:
		return fmt.Errorf("unsupported silence mode")
	}
	result := db.Model(&models.LoadNotificationState{}).
		Where("notification_id = ? AND client = ? AND alert_active = ?", notificationID, client, true).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func loadAlertStateCurrent(state models.LoadNotificationState, now time.Time) bool {
	if !state.AlertActive || state.Notification.Id == 0 || state.Notification.Interval <= 0 || state.LastEvaluatedAt.IsZero() {
		return false
	}
	if state.RuleFingerprint != models.LoadNotificationRuleFingerprint(state.Notification) {
		return false
	}
	freshFor := 2 * time.Duration(state.Notification.Interval) * time.Minute
	if freshFor < 2*time.Minute {
		freshFor = 2 * time.Minute
	}
	return state.LastEvaluatedAt.Add(freshFor).After(now.UTC())
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
