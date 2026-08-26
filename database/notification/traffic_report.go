package notification

import (
	"context"
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	dailyReportRetentionDays = trafficledger.MetricSafetyRetentionDays
)

func validateTrafficReportNotification(notification models.TrafficReportNotification) error {
	if notification.Client == "" {
		return fmt.Errorf("client UUID cannot be empty")
	}
	if notification.Enable && !notification.Daily && !notification.Weekly && !notification.Monthly {
		return fmt.Errorf("at least one cadence must be selected when enabling traffic reports")
	}
	if notification.Enable && !notification.IncludeTraffic && !notification.IncludeBilling {
		return fmt.Errorf("at least one report content type must be selected when enabling traffic reports")
	}
	return nil
}

func ValidateTrafficReportNotifications(notifications []models.TrafficReportNotification) error {
	for _, notification := range notifications {
		if err := validateTrafficReportNotification(notification); err != nil {
			return err
		}
	}
	return nil
}

func buildEnabledTrafficReportNotifications(uuids []string, existing []models.TrafficReportNotification) ([]models.TrafficReportNotification, error) {
	if len(uuids) == 0 {
		return nil, fmt.Errorf("at least one client UUID is required")
	}

	existingByClient := make(map[string]models.TrafficReportNotification, len(existing))
	for _, notification := range existing {
		existingByClient[notification.Client] = notification
	}

	notifications := make([]models.TrafficReportNotification, 0, len(uuids))
	for _, uuid := range uuids {
		if uuid == "" {
			return nil, fmt.Errorf("client UUID cannot be empty")
		}
		existingNotification, ok := existingByClient[uuid]
		if !ok || (!existingNotification.Daily && !existingNotification.Weekly && !existingNotification.Monthly) {
			return nil, fmt.Errorf("at least one cadence must be selected when enabling traffic reports")
		}
		if !existingNotification.IncludeTraffic && !existingNotification.IncludeBilling {
			return nil, fmt.Errorf("at least one report content type must be selected when enabling traffic reports")
		}
		notifications = append(notifications, models.TrafficReportNotification{
			Client: uuid,
			Enable: true,
		})
	}

	return notifications, nil
}

// ListTrafficReportNotifications 获取所有流量定时报告配置（关联客户端信息）
func ListTrafficReportNotifications() ([]models.TrafficReportNotification, error) {
	db := dbcore.GetDBInstance()
	var notifications []models.TrafficReportNotification
	err := db.Model(&models.TrafficReportNotification{}).Preload("ClientInfo").Find(&notifications).Error
	return notifications, err
}

// EditTrafficReportNotifications 批量更新流量定时报告配置
func EditTrafficReportNotifications(notifications []models.TrafficReportNotification) error {
	if err := ValidateTrafficReportNotifications(notifications); err != nil {
		return err
	}
	db := dbcore.GetDBInstance()
	if err := upsertTrafficReportNotifications(db, notifications); err != nil {
		return err
	}
	return EnsureTrafficReportMetricRetention(context.Background())
}

func upsertTrafficReportNotifications(db *gorm.DB, notifications []models.TrafficReportNotification) error {
	if len(notifications) == 0 {
		return nil
	}

	rows := make([]map[string]interface{}, 0, len(notifications))
	for _, notification := range notifications {
		rows = append(rows, map[string]interface{}{
			"client":          notification.Client,
			"enable":          notification.Enable,
			"daily":           notification.Daily,
			"weekly":          notification.Weekly,
			"monthly":         notification.Monthly,
			"include_traffic": notification.IncludeTraffic,
			"include_billing": notification.IncludeBilling,
		})
	}

	return db.Model(&models.TrafficReportNotification{}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}},
			DoUpdates: clause.AssignmentColumns([]string{"enable", "daily", "weekly", "monthly", "include_traffic", "include_billing"}),
		}).
		Create(rows).Error
}

// EnableTrafficReportNotifications 批量启用（仅更新 enable 字段）
func EnableTrafficReportNotifications(uuids []string) error {
	if len(uuids) == 0 {
		return fmt.Errorf("at least one client UUID is required")
	}

	db := dbcore.GetDBInstance()
	var existing []models.TrafficReportNotification
	if err := db.Where("client IN ?", uuids).Find(&existing).Error; err != nil {
		return err
	}
	notifications, err := buildEnabledTrafficReportNotifications(uuids, existing)
	if err != nil {
		return err
	}
	if err := db.Model(&models.TrafficReportNotification{}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}},
			DoUpdates: clause.AssignmentColumns([]string{"enable"}),
		}).
		Select("client", "enable").
		Create(notifications).Error; err != nil {
		return err
	}
	return EnsureTrafficReportMetricRetention(context.Background())
}

// DisableTrafficReportNotifications 批量禁用
func DisableTrafficReportNotifications(uuids []string) error {
	if len(uuids) == 0 {
		return fmt.Errorf("at least one client UUID is required")
	}
	db := dbcore.GetDBInstance()
	var notifications []models.TrafficReportNotification
	for _, uuid := range uuids {
		if uuid == "" {
			return fmt.Errorf("client UUID cannot be empty")
		}
		notifications = append(notifications, models.TrafficReportNotification{
			Client: uuid,
			Enable: false,
		})
	}
	return db.Model(&models.TrafficReportNotification{}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "client"}},
			DoUpdates: clause.AssignmentColumns([]string{"enable"}),
		}).
		Select("client", "enable").
		Create(notifications).Error
}

// GetEnabledTrafficReportByType 获取启用了指定类型报告的客户端配置
func GetEnabledTrafficReportByType(daily, weekly, monthly bool) ([]models.TrafficReportNotification, error) {
	db := dbcore.GetDBInstance()
	var notifications []models.TrafficReportNotification
	query := db.Model(&models.TrafficReportNotification{}).Where("enable = ?", true)
	if daily {
		query = query.Where("daily = ?", true)
	} else if weekly {
		query = query.Where("weekly = ?", true)
	} else if monthly {
		query = query.Where("monthly = ?", true)
	}
	err := query.Find(&notifications).Error
	return notifications, err
}

func RequiredTrafficReportRetentionDays(notifications []models.TrafficReportNotification) int {
	for _, notification := range notifications {
		if notification.Enable && (notification.Daily || notification.Weekly || notification.Monthly) {
			return dailyReportRetentionDays
		}
	}
	return 0
}

func trafficReportRetentionTarget(currentDays, requiredDays, baselineDays int, backfillComplete bool) (int, bool) {
	if currentDays == 0 && requiredDays == 0 {
		return currentDays, false
	}
	desiredDays := requiredDays
	if baselineDays > desiredDays {
		desiredDays = baselineDays
	}
	if requiredDays > 0 && currentDays < desiredDays {
		return desiredDays, true
	}
	if !backfillComplete || currentDays <= desiredDays {
		return currentDays, false
	}
	// Only the exact legacy auto-raised weekly/monthly values can be safely
	// restored. Other longer values are considered administrator choices.
	if currentDays == 8 || currentDays == 35 {
		return desiredDays, true
	}
	return currentDays, false
}

func commonMetricRetentionDays(ctx context.Context) (int, error) {
	store := metricstore.GetStore()
	if store == nil {
		return 0, fmt.Errorf("metric store is not initialized")
	}
	metricNames := []string{
		metricstore.MetricCPU,
		metricstore.MetricRAM,
		metricstore.MetricSwap,
		metricstore.MetricLoad,
		metricstore.MetricDisk,
		metricstore.MetricNetIn,
		metricstore.MetricNetOut,
		metricstore.MetricProcess,
		metricstore.MetricConnections,
		metricstore.MetricConnectionsUDP,
	}
	counts := make(map[int]int)
	for _, metricName := range metricNames {
		definition, err := store.GetMetric(ctx, metricName)
		if err != nil {
			return 0, fmt.Errorf("get retention for %s: %w", metricName, err)
		}
		if definition.RetentionDays > 0 {
			counts[definition.RetentionDays]++
		}
	}
	bestDays, bestCount := 1, 0
	for days, count := range counts {
		if count > bestCount || (count == bestCount && days < bestDays) {
			bestDays, bestCount = days, count
		}
	}
	return bestDays, nil
}

var trafficReportMetricNames = []string{
	metricstore.MetricTrafficUp,
	metricstore.MetricTrafficDown,
	metricstore.MetricNetTotalUp,
	metricstore.MetricNetTotalDown,
}

// EnsureTrafficReportMetricRetention first creates the independent daily
// ledger, then restores only old report-imposed 8/35-day retention values.
// Failed backfills never shorten the source metric retention.
func EnsureTrafficReportMetricRetention(ctx context.Context) error {
	db := dbcore.GetDBInstance()
	var notifications []models.TrafficReportNotification
	if err := db.WithContext(ctx).Where("enable = ?", true).Find(&notifications).Error; err != nil {
		return err
	}
	requiredDays := RequiredTrafficReportRetentionDays(notifications)
	store := metricstore.GetStore()
	if store == nil {
		return fmt.Errorf("metric store is not initialized")
	}
	baselineDays, err := commonMetricRetentionDays(ctx)
	if err != nil {
		return err
	}

	// Raising retention is always safe and protects the next settlement while
	// the one-time ledger backfill is still running.
	for _, metricName := range trafficReportMetricNames {
		definition, err := store.GetMetric(ctx, metricName)
		if err != nil {
			return fmt.Errorf("get retention for %s: %w", metricName, err)
		}
		targetDays, changed := trafficReportRetentionTarget(definition.RetentionDays, requiredDays, baselineDays, false)
		if !changed {
			continue
		}
		if _, err := store.SetMetricRetention(ctx, metricName, targetDays); err != nil {
			return fmt.Errorf("set retention for %s: %w", metricName, err)
		}
	}

	if err := trafficledger.BackfillEnabledHistory(ctx, db, time.Now().UTC()); err != nil {
		return fmt.Errorf("backfill daily traffic ledger: %w", err)
	}

	for _, metricName := range trafficReportMetricNames {
		definition, err := store.GetMetric(ctx, metricName)
		if err != nil {
			return fmt.Errorf("get retention for %s after ledger backfill: %w", metricName, err)
		}
		targetDays, changed := trafficReportRetentionTarget(definition.RetentionDays, requiredDays, baselineDays, true)
		if !changed {
			continue
		}
		if _, err := store.SetMetricRetention(ctx, metricName, targetDays); err != nil {
			return fmt.Errorf("restore retention for %s: %w", metricName, err)
		}
	}
	return nil
}
