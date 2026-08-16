package notificationdefaults

import (
	"fmt"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/config"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OfflineNotificationDefaultConfig struct {
	Enabled     bool `json:"enabled"`
	GracePeriod int  `json:"grace_period"`
}

type PingLossNotificationDefaultConfig struct {
	Enabled         bool    `json:"enabled"`
	WindowSeconds   int     `json:"window_seconds"`
	LossThreshold   float64 `json:"loss_threshold"`
	MinimumSamples  int     `json:"minimum_samples"`
	CooldownSeconds int     `json:"cooldown_seconds"`
}

type TrafficReportDefaultConfig struct {
	Enabled        bool `json:"enabled"`
	Daily          bool `json:"daily"`
	Weekly         bool `json:"weekly"`
	Monthly        bool `json:"monthly"`
	IncludeTraffic bool `json:"include_traffic"`
	IncludeBilling bool `json:"include_billing"`
}

var defaultOfflineNotificationConfig = OfflineNotificationDefaultConfig{
	Enabled:     false,
	GracePeriod: 180,
}

var defaultPingLossNotificationConfig = PingLossNotificationDefaultConfig{
	Enabled:         false,
	WindowSeconds:   60,
	LossThreshold:   5,
	MinimumSamples:  1,
	CooldownSeconds: 300,
}

var defaultTrafficReportConfig = TrafficReportDefaultConfig{
	Enabled:        false,
	Daily:          true,
	Weekly:         false,
	Monthly:        false,
	IncludeTraffic: true,
	IncludeBilling: false,
}

var reconcileTrafficReportRetention = func() error { return nil }

func RegisterTrafficReportRetentionReconciler(reconcile func() error) {
	if reconcile != nil {
		reconcileTrafficReportRetention = reconcile
	}
}

func GetOfflineNotificationDefaultConfig() (OfflineNotificationDefaultConfig, error) {
	return config.GetAs[OfflineNotificationDefaultConfig](config.OfflineNotificationDefaultKey, defaultOfflineNotificationConfig)
}

func SetOfflineNotificationDefaultConfig(value OfflineNotificationDefaultConfig) error {
	if value.GracePeriod <= 0 {
		return fmt.Errorf("grace period must be a positive integer")
	}
	return config.Set(config.OfflineNotificationDefaultKey, value)
}

func GetPingLossNotificationDefaultConfig() (PingLossNotificationDefaultConfig, error) {
	return config.GetAs[PingLossNotificationDefaultConfig](config.PingLossNotificationDefaultKey, defaultPingLossNotificationConfig)
}

func SetPingLossNotificationDefaultConfig(value PingLossNotificationDefaultConfig) error {
	if err := validatePingLossNotificationDefaultConfig(value); err != nil {
		return err
	}
	return config.Set(config.PingLossNotificationDefaultKey, value)
}

func GetTrafficReportDefaultConfig() (TrafficReportDefaultConfig, error) {
	return config.GetAs[TrafficReportDefaultConfig](config.TrafficReportDefaultKey, defaultTrafficReportConfig)
}

func SetTrafficReportDefaultConfig(value TrafficReportDefaultConfig) error {
	if err := validateTrafficReportDefaultConfig(value); err != nil {
		return err
	}
	return config.Set(config.TrafficReportDefaultKey, value)
}

func validatePingLossNotificationDefaultConfig(value PingLossNotificationDefaultConfig) error {
	if value.WindowSeconds < 60 || value.WindowSeconds > 24*60*60 {
		return fmt.Errorf("window must be between 60 and 86400 seconds")
	}
	if value.LossThreshold <= 0 || value.LossThreshold > 100 {
		return fmt.Errorf("loss threshold must be greater than 0 and at most 100")
	}
	if value.MinimumSamples < 1 || value.MinimumSamples > 100000 {
		return fmt.Errorf("minimum samples must be between 1 and 100000")
	}
	if value.CooldownSeconds < 60 || value.CooldownSeconds > 7*24*60*60 {
		return fmt.Errorf("cooldown must be between 60 and 604800 seconds")
	}
	return nil
}

func validateTrafficReportDefaultConfig(value TrafficReportDefaultConfig) error {
	if value.Enabled && !value.Daily && !value.Weekly && !value.Monthly {
		return fmt.Errorf("at least one cadence must be selected when enabling traffic reports")
	}
	if value.Enabled && !value.IncludeTraffic && !value.IncludeBilling {
		return fmt.Errorf("at least one report content type must be selected when enabling traffic reports")
	}
	return nil
}

func ApplyDefaultsToNewClient(clientUUID string) error {
	trafficReportApplied, err := applyDefaultsToNewClient(dbcore.GetDBInstance(), clientUUID)
	if err != nil {
		return err
	}
	if trafficReportApplied {
		return reconcileTrafficReportRetention()
	}
	return nil
}

func applyDefaultsToNewClient(db *gorm.DB, clientUUID string) (bool, error) {
	if clientUUID == "" {
		return false, nil
	}
	offlineConfig, err := GetOfflineNotificationDefaultConfig()
	if err != nil {
		return false, fmt.Errorf("load offline notification default: %w", err)
	}
	pingLossConfig, err := GetPingLossNotificationDefaultConfig()
	if err != nil {
		return false, fmt.Errorf("load ping loss notification default: %w", err)
	}
	trafficReportConfig, err := GetTrafficReportDefaultConfig()
	if err != nil {
		return false, fmt.Errorf("load traffic report default: %w", err)
	}
	trafficReportApplied := false

	err = db.Transaction(func(tx *gorm.DB) error {
		if offlineConfig.Enabled {
			offline := models.OfflineNotification{
				Client:      clientUUID,
				Enable:      true,
				GracePeriod: offlineConfig.GracePeriod,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client"}},
				DoNothing: true,
			}).Select("client", "enable", "grace_period").Create(&offline).Error; err != nil {
				return fmt.Errorf("apply offline notification default: %w", err)
			}
		}

		if pingLossConfig.Enabled {
			var pingTasks []models.PingTask
			if err := tx.Where("clients LIKE ?", `%"`+clientUUID+`"%`).Find(&pingTasks).Error; err != nil {
				return fmt.Errorf("find ping tasks for notification defaults: %w", err)
			}
			for _, task := range pingTasks {
				if !task.AppliesToClient(clientUUID) {
					continue
				}
				if err := applyPingLossDefaultToClients(tx, pingLossConfig, task.Id, []string{clientUUID}); err != nil {
					return err
				}
			}
		}

		if trafficReportConfig.Enabled {
			candidate := models.TrafficReportNotification{
				Client:         clientUUID,
				Enable:         true,
				Daily:          trafficReportConfig.Daily,
				Weekly:         trafficReportConfig.Weekly,
				Monthly:        trafficReportConfig.Monthly,
				IncludeTraffic: trafficReportConfig.IncludeTraffic,
				IncludeBilling: trafficReportConfig.IncludeBilling,
			}
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client"}},
				DoNothing: true,
			}).Select(
				"client", "enable", "daily", "weekly", "monthly", "include_traffic", "include_billing",
			).Create(&candidate)
			if result.Error != nil {
				return fmt.Errorf("apply traffic report default: %w", result.Error)
			}
			trafficReportApplied = result.RowsAffected > 0
		}
		return nil
	})
	return trafficReportApplied, err
}

// ApplyPingLossDefaultsToTaskClients 把已保存的延迟监测告警默认配置套到新的任务/服务器组合上，已有告警行保持不变。
func ApplyPingLossDefaultsToTaskClients(db *gorm.DB, taskID uint, clients []string) error {
	if db == nil || taskID == 0 || len(clients) == 0 {
		return nil
	}
	cfg, err := GetPingLossNotificationDefaultConfig()
	if err != nil {
		return fmt.Errorf("load ping loss notification default: %w", err)
	}
	return ApplyLoadedPingLossDefaultsToTaskClients(db, cfg, taskID, clients)
}

// ApplyLoadedPingLossDefaultsToTaskClients 使用已经读好的默认配置写入新的任务/服务器组合。
// 调用方必须在事务外完成 GetPingLossNotificationDefaultConfig，避免 SQLite 单连接卡死。
func ApplyLoadedPingLossDefaultsToTaskClients(db *gorm.DB, cfg PingLossNotificationDefaultConfig, taskID uint, clients []string) error {
	return applyPingLossDefaultToClients(db, cfg, taskID, clients)
}

func applyPingLossDefaultToClients(tx *gorm.DB, cfg PingLossNotificationDefaultConfig, taskID uint, clients []string) error {
	if tx == nil || !cfg.Enabled || taskID == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(clients))
	for _, clientUUID := range clients {
		if clientUUID == "" {
			continue
		}
		if _, ok := seen[clientUUID]; ok {
			continue
		}
		seen[clientUUID] = struct{}{}
		candidate := models.PingLossNotification{
			Client:          clientUUID,
			TaskId:          taskID,
			Enable:          true,
			WindowSeconds:   cfg.WindowSeconds,
			LossThreshold:   cfg.LossThreshold,
			MinimumSamples:  cfg.MinimumSamples,
			CooldownSeconds: cfg.CooldownSeconds,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}, {Name: "task_id"}},
			DoNothing: true,
		}).Select(
			"client", "task_id", "enable", "window_seconds", "loss_threshold",
			"minimum_samples", "cooldown_seconds",
		).Create(&candidate).Error; err != nil {
			return fmt.Errorf("apply ping loss notification default: %w", err)
		}
	}
	return nil
}
