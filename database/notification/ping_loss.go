package notification

import (
	"errors"
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

func ValidatePingLossNotification(notification models.PingLossNotification) error {
	if notification.Client == "" {
		return fmt.Errorf("client UUID cannot be empty")
	}
	if notification.TaskId == 0 {
		return fmt.Errorf("ping task is required")
	}
	if notification.Enable && !notification.LossEnabled && !notification.LatencyEnabled {
		return fmt.Errorf("enable packet-loss or latency anomaly alerts")
	}
	if err := validatePingLossFields(notification); err != nil {
		return err
	}
	if notification.LatencyEnabled {
		return validateLatencyAnomalyFields(notification)
	}
	return nil
}

func validatePingLossFields(notification models.PingLossNotification) error {
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

func validateLatencyAnomalyFields(notification models.PingLossNotification) error {
	if notification.LatencyWindowSeconds < 60 || notification.LatencyWindowSeconds > 24*60*60 {
		return fmt.Errorf("latency window must be between 60 and 86400 seconds")
	}
	if notification.LatencyMinimumSamples < 1 || notification.LatencyMinimumSamples > 100000 {
		return fmt.Errorf("latency minimum samples must be between 1 and 100000")
	}
	if notification.LatencyCooldownSeconds < 60 || notification.LatencyCooldownSeconds > 7*24*60*60 {
		return fmt.Errorf("latency cooldown must be between 60 and 604800 seconds")
	}
	if notification.AdaptiveBaselineEnabled {
		if notification.AdaptiveLowerDeviationPercent <= 0 || notification.AdaptiveLowerDeviationPercent >= 100 {
			return fmt.Errorf("lower deviation percent must be greater than 0 and less than 100")
		}
		if notification.AdaptiveUpperDeviationPercent <= 0 || notification.AdaptiveUpperDeviationPercent > 1000 {
			return fmt.Errorf("upper deviation percent must be greater than 0 and at most 1000")
		}
		if notification.BaselineWindowSeconds < notification.LatencyWindowSeconds || notification.BaselineWindowSeconds > 30*24*60*60 {
			return fmt.Errorf("baseline window must be between the latency window and 30 days")
		}
		if notification.BaselineMinimumSamples < 1 || notification.BaselineMinimumSamples > 1000000 {
			return fmt.Errorf("baseline minimum samples must be between 1 and 1000000")
		}
		return nil
	}
	if notification.FixedBaselineMs <= 0 {
		return fmt.Errorf("fixed baseline must be greater than 0")
	}
	if notification.LowLatencyThresholdMs < 0 {
		return fmt.Errorf("low latency threshold must be at least 0")
	}
	low, high, ok := metricstore.LatencyComparisonBand(
		notification.LowLatencyThresholdMs,
		notification.HighLatencyThresholdMs,
		(notification.LowLatencyThresholdMs+notification.HighLatencyThresholdMs)/2,
	)
	if !ok {
		return fmt.Errorf("high latency threshold must be greater than the low latency threshold")
	}
	baseline := metricstore.RoundLatencyMS(notification.FixedBaselineMs)
	if baseline <= low || baseline >= high {
		return fmt.Errorf("fixed baseline must be between the low and high latency thresholds")
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

func loadPingLossTask(db *gorm.DB, taskID uint) (models.PingTask, error) {
	var task models.PingTask
	if err := db.Where("id = ?", taskID).First(&task).Error; err != nil {
		return models.PingTask{}, fmt.Errorf("ping task does not exist: %w", err)
	}
	return task, nil
}

func AddPingLossNotification(notification models.PingLossNotification) (uint, error) {
	if err := ValidatePingLossNotification(notification); err != nil {
		return 0, err
	}
	db := dbcore.GetDBInstance()
	if err := validatePingLossTarget(db, notification); err != nil {
		return 0, err
	}
	task, err := loadPingLossTask(db, notification.TaskId)
	if err != nil {
		return 0, err
	}
	candidate := resetPingLossRuntimeState(notification)
	candidate.Id = 0
	applyAdaptiveBaselineFingerprint(&candidate, task)
	if err := db.Select(pingLossCreateColumns).Create(&candidate).Error; err != nil {
		return 0, err
	}
	return candidate.Id, nil
}

func pingLossNotificationUpdates(notification *models.PingLossNotification) map[string]any {
	return map[string]any{
		"client":                           notification.Client,
		"task_id":                          notification.TaskId,
		"enable":                           notification.Enable,
		"loss_enabled":                     notification.LossEnabled,
		"window_seconds":                   notification.WindowSeconds,
		"loss_threshold":                   notification.LossThreshold,
		"minimum_samples":                  notification.MinimumSamples,
		"cooldown_seconds":                 notification.CooldownSeconds,
		"latency_enabled":                  notification.LatencyEnabled,
		"adaptive_baseline_enabled":        notification.AdaptiveBaselineEnabled,
		"latency_window_seconds":           notification.LatencyWindowSeconds,
		"latency_minimum_samples":          notification.LatencyMinimumSamples,
		"latency_cooldown_seconds":         notification.LatencyCooldownSeconds,
		"fixed_baseline_ms":                notification.FixedBaselineMs,
		"low_latency_threshold_ms":         notification.LowLatencyThresholdMs,
		"high_latency_threshold_ms":        notification.HighLatencyThresholdMs,
		"adaptive_lower_deviation_percent": notification.AdaptiveLowerDeviationPercent,
		"adaptive_upper_deviation_percent": notification.AdaptiveUpperDeviationPercent,
		"baseline_window_seconds":          notification.BaselineWindowSeconds,
		"baseline_minimum_samples":         notification.BaselineMinimumSamples,
	}
}

func pingLossConfigSideEffects(existing, next models.PingLossNotification, task models.PingTask, now time.Time) map[string]any {
	updates := pingLossNotificationUpdates(&next)
	if !next.Enable || !next.LossEnabled {
		updates["alert_active"] = false
		updates["loss_incident_notified"] = false
	} else if existing.Id != 0 && lossJudgmentChanged(existing, next) {
		updates["alert_active"] = false
		updates["loss_incident_notified"] = false
	}

	latencyEventMustEnd := !next.Enable || !next.LatencyEnabled || (existing.Id != 0 && latencyJudgmentChanged(existing, next))
	if !next.Enable || !next.LatencyEnabled {
		updates["latency_alert_state"] = models.LatencyAlertNormal
		updates["latency_active_since"] = nil
		updates["latency_incident_notified"] = false
	} else if existing.Id != 0 && latencyJudgmentChanged(existing, next) {
		updates["latency_alert_state"] = models.LatencyAlertNormal
		updates["latency_active_since"] = nil
		updates["latency_last_notified"] = nil
		updates["latency_incident_notified"] = false
	}

	fp := models.AdaptiveBaselineFingerprint(task.Type, task.Target, next.BaselineWindowSeconds, next.BaselineMinimumSamples)
	if shouldResetAdaptiveBaseline(existing, next, fp) {
		updates["adaptive_baseline_ms"] = nil
		updates["adaptive_baseline_status"] = models.AdaptiveBaselineWarming
		updates["adaptive_baseline_sample_count"] = 0
		updates["adaptive_baseline_updated_at"] = nil
		updates["adaptive_baseline_resume_at"] = nil
		updates["adaptive_baseline_fingerprint"] = fp
		return updates
	}
	if shouldEnterAdaptiveRecoveryHold(existing, next, latencyEventMustEnd) {
		resume := now.UTC().Add(time.Duration(effectiveLatencyWindowSeconds(existing, next)) * time.Second)
		updates["adaptive_baseline_status"] = models.AdaptiveBaselineRecoveryHold
		updates["adaptive_baseline_resume_at"] = resume
	}
	return updates
}

func shouldEnterAdaptiveRecoveryHold(existing, next models.PingLossNotification, latencyEventMustEnd bool) bool {
	if !next.AdaptiveBaselineEnabled || !existing.HasAdaptiveBaseline() {
		return false
	}
	if existing.NormalizedLatencyAlertState() == models.LatencyAlertNormal &&
		existing.NormalizedAdaptiveBaselineStatus() == models.AdaptiveBaselineFrozen {
		return true
	}
	return latencyEventMustEnd && existingAdaptiveIncidentActive(existing)
}

func existingAdaptiveIncidentActive(existing models.PingLossNotification) bool {
	switch existing.NormalizedLatencyAlertState() {
	case models.LatencyAlertHigh, models.LatencyAlertLow:
		return true
	default:
		return existing.NormalizedAdaptiveBaselineStatus() == models.AdaptiveBaselineFrozen
	}
}

func effectiveLatencyWindowSeconds(existing, next models.PingLossNotification) int {
	if validLatencyWindowSeconds(next.LatencyWindowSeconds) {
		return next.LatencyWindowSeconds
	}
	if validLatencyWindowSeconds(existing.LatencyWindowSeconds) {
		return existing.LatencyWindowSeconds
	}
	return 300
}

func validLatencyWindowSeconds(seconds int) bool {
	return seconds >= 60 && seconds <= 24*60*60
}

func pingLossSQLUpdates(updates map[string]any) map[string]any {
	sqlUpdates := make(map[string]any, len(updates))
	for key, value := range updates {
		if value == nil {
			sqlUpdates[key] = gorm.Expr("NULL")
			continue
		}
		sqlUpdates[key] = value
	}
	return sqlUpdates
}

func lossJudgmentChanged(existing, next models.PingLossNotification) bool {
	return existing.WindowSeconds != next.WindowSeconds ||
		existing.LossThreshold != next.LossThreshold ||
		existing.MinimumSamples != next.MinimumSamples ||
		existing.CooldownSeconds != next.CooldownSeconds
}

func latencyJudgmentChanged(existing, next models.PingLossNotification) bool {
	return existing.AdaptiveBaselineEnabled != next.AdaptiveBaselineEnabled ||
		existing.LatencyWindowSeconds != next.LatencyWindowSeconds ||
		existing.LatencyMinimumSamples != next.LatencyMinimumSamples ||
		existing.LatencyCooldownSeconds != next.LatencyCooldownSeconds ||
		existing.FixedBaselineMs != next.FixedBaselineMs ||
		existing.LowLatencyThresholdMs != next.LowLatencyThresholdMs ||
		existing.HighLatencyThresholdMs != next.HighLatencyThresholdMs ||
		existing.AdaptiveLowerDeviationPercent != next.AdaptiveLowerDeviationPercent ||
		existing.AdaptiveUpperDeviationPercent != next.AdaptiveUpperDeviationPercent ||
		existing.BaselineWindowSeconds != next.BaselineWindowSeconds ||
		existing.BaselineMinimumSamples != next.BaselineMinimumSamples
}

func shouldResetAdaptiveBaseline(existing, next models.PingLossNotification, fingerprint string) bool {
	if !next.LatencyEnabled || !next.AdaptiveBaselineEnabled {
		return false
	}
	if existing.Id == 0 {
		return true
	}
	if !existing.AdaptiveBaselineEnabled {
		return true
	}
	if existing.BaselineWindowSeconds != next.BaselineWindowSeconds ||
		existing.BaselineMinimumSamples != next.BaselineMinimumSamples {
		return true
	}
	return existing.AdaptiveBaselineFingerprint != "" && existing.AdaptiveBaselineFingerprint != fingerprint
}

func resetPingLossRuntimeState(notification models.PingLossNotification) models.PingLossNotification {
	notification.LastNotified = nil
	notification.AlertActive = false
	notification.LossIncidentNotified = false
	notification.LatencyAlertState = models.LatencyAlertNormal
	notification.LatencyIncidentNotified = false
	notification.LatencyActiveSince = nil
	notification.LatencyLastNotified = nil
	notification.LatencyLastEvaluatedAt = nil
	notification.LatencyLatestAverageMs = 0
	notification.LatencySuccessfulSamples = 0
	notification.AdaptiveBaselineMs = nil
	notification.AdaptiveBaselineStatus = models.AdaptiveBaselineWarming
	notification.AdaptiveBaselineSampleCount = 0
	notification.AdaptiveBaselineUpdatedAt = nil
	notification.AdaptiveBaselineResumeAt = nil
	notification.AdaptiveBaselineFingerprint = ""
	notification.AdaptiveBaselineStartedAt = nil
	return applyLatencyFieldDefaults(notification)
}

func applyLatencyFieldDefaults(notification models.PingLossNotification) models.PingLossNotification {
	if notification.LatencyWindowSeconds == 0 {
		notification.LatencyWindowSeconds = 300
	}
	if notification.LatencyMinimumSamples == 0 {
		notification.LatencyMinimumSamples = 30
	}
	if notification.LatencyCooldownSeconds == 0 {
		notification.LatencyCooldownSeconds = 1800
	}
	if notification.AdaptiveLowerDeviationPercent == 0 {
		notification.AdaptiveLowerDeviationPercent = 25
	}
	if notification.AdaptiveUpperDeviationPercent == 0 {
		notification.AdaptiveUpperDeviationPercent = 25
	}
	if notification.BaselineWindowSeconds == 0 {
		notification.BaselineWindowSeconds = 86400
	}
	if notification.BaselineMinimumSamples == 0 {
		notification.BaselineMinimumSamples = 30
	}
	if notification.LatencyAlertState == "" {
		notification.LatencyAlertState = models.LatencyAlertNormal
	}
	if notification.AdaptiveBaselineStatus == "" {
		notification.AdaptiveBaselineStatus = models.AdaptiveBaselineWarming
	}
	return notification
}

func applyAdaptiveBaselineFingerprint(notification *models.PingLossNotification, task models.PingTask) {
	if notification == nil || !notification.LatencyEnabled || !notification.AdaptiveBaselineEnabled {
		return
	}
	notification.AdaptiveBaselineFingerprint = models.AdaptiveBaselineFingerprint(
		task.Type, task.Target, notification.BaselineWindowSeconds, notification.BaselineMinimumSamples,
	)
}

var pingLossCreateColumns = []string{
	"client", "task_id", "enable", "loss_enabled", "window_seconds", "loss_threshold",
	"minimum_samples", "cooldown_seconds", "last_notified", "alert_active",
	"loss_incident_notified",
	"latency_enabled", "adaptive_baseline_enabled", "latency_window_seconds",
	"latency_minimum_samples", "latency_cooldown_seconds", "fixed_baseline_ms",
	"low_latency_threshold_ms", "high_latency_threshold_ms", "adaptive_lower_deviation_percent",
	"adaptive_upper_deviation_percent", "baseline_window_seconds",
	"baseline_minimum_samples", "latency_alert_state", "latency_incident_notified",
	"adaptive_baseline_status",
	"adaptive_baseline_sample_count", "adaptive_baseline_fingerprint",
}

func EditPingLossNotifications(notifications []*models.PingLossNotification) error {
	if len(notifications) == 0 {
		return fmt.Errorf("at least one notification is required")
	}
	db := dbcore.GetDBInstance()
	return db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
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
			var existing models.PingLossNotification
			if err := tx.Where("id = ?", notification.Id).First(&existing).Error; err != nil {
				return err
			}
			task, err := loadPingLossTask(tx, notification.TaskId)
			if err != nil {
				return err
			}
			result := tx.Model(&models.PingLossNotification{}).Where("id = ?", notification.Id).
				Updates(pingLossSQLUpdates(pingLossConfigSideEffects(existing, *notification, task, now)))
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
// configurations atomically. Existing targets keep their notification history
// unless a judgment-parameter change starts a new incident.
func UpsertPingLossNotifications(notifications []*models.PingLossNotification) error {
	return upsertPingLossNotifications(dbcore.GetDBInstance(), notifications)
}

func upsertPingLossNotifications(db *gorm.DB, notifications []*models.PingLossNotification) error {
	if len(notifications) == 0 {
		return fmt.Errorf("at least one notification is required")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
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
			task, err := loadPingLossTask(tx, notification.TaskId)
			if err != nil {
				return err
			}

			var existing models.PingLossNotification
			err = tx.Where(
				"client = ? AND task_id = ?",
				notification.Client,
				notification.TaskId,
			).First(&existing).Error
			switch {
			case err == nil:
				if err := tx.Model(&models.PingLossNotification{}).
					Where("id = ?", existing.Id).
					Updates(pingLossSQLUpdates(pingLossConfigSideEffects(existing, *notification, task, now))).Error; err != nil {
					return err
				}
			case errors.Is(err, gorm.ErrRecordNotFound):
				candidate := resetPingLossRuntimeState(*notification)
				candidate.Id = 0
				applyAdaptiveBaselineFingerprint(&candidate, task)
				if err := tx.Select(pingLossCreateColumns).Create(&candidate).Error; err != nil {
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
