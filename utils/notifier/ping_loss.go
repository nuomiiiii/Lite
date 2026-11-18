package notifier

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/alertsilence"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/corn"
	"github.com/nuomiiiii/lite/pkg/metric"
	"github.com/nuomiiiii/lite/utils/messageSender"
	"gorm.io/gorm"
)

const pingLossCheckInterval = 15 * time.Second

var pingLossCheckMu sync.Mutex

type pingLossStats = metricstore.PingLossStats

type pingBaselineCandidateQuery func(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time) (metricstore.PingBaselineCandidate, error)

var queryPingBaselineCandidate pingBaselineCandidateQuery = metricstore.QueryPingBaselineCandidate

var sendPingHealthEvent = messageSender.SendEvent

type pingLossNotificationAction uint8

const (
	pingLossNotificationNone pingLossNotificationAction = iota
	pingLossNotificationAlert
	pingLossNotificationRecovery
	pingLossNotificationSilentEnd
)

func InitPingLossNotificationSchedule() {
	if err := corn.AddFunc("ping-loss-notification", corn.Every(pingLossCheckInterval), CheckPingLossNotifications); err != nil {
		log.Printf("Failed to register ping loss notification job: %v", err)
	}
}

func CheckPingLossNotifications() {
	if !pingLossCheckMu.TryLock() {
		return
	}
	defer pingLossCheckMu.Unlock()

	enabled, err := config.GetAs[bool](config.NotificationEnabledKey, false)
	if err != nil || !enabled {
		return
	}

	store := metricstore.GetStore()
	if store == nil {
		log.Printf("Failed to check ping loss notifications: metric store is not initialized")
		return
	}

	db := dbcore.GetDBInstance()
	var notifications []models.PingLossNotification
	if err := db.Preload("ClientInfo").Preload("Task").Where("enable = ?", true).Find(&notifications).Error; err != nil {
		log.Printf("Failed to load ping loss notifications: %v", err)
		return
	}

	now := time.Now().UTC()
	for _, notification := range notifications {
		lossStats := pingLossStats{}
		lossAction := pingLossNotificationNone
		latencyStats := metricstore.PingHealthStats{}
		latencyEval := pingLatencyEvaluation{Notification: notification, Action: pingLatencyNotificationNone}
		latencyEvaluated := false

		sharedHealthLoaded := false
		if notification.LatencyEnabled {
			windowStart := now.Add(-time.Duration(notification.LatencyWindowSeconds) * time.Second)
			stats, err := metricstore.QueryPingHealthStats(context.Background(), store, notification.Client, notification.TaskId, windowStart, now)
			if err != nil {
				log.Printf("Failed to compute ping latency for notification %d: %v", notification.Id, err)
			} else {
				latencyStats = stats
				sharedHealthLoaded = true
				if notification.LossEnabled && notification.WindowSeconds == notification.LatencyWindowSeconds {
					lossStats = pingLossStats{Total: stats.Total, Lost: stats.Lost}
					lossAction = evaluatePingLossNotification(notification, lossStats, now)
				}
				prepared, candidate, _, baselineErr := loadAdaptiveBaselineCandidate(context.Background(), store, notification, now)
				if baselineErr != nil {
					log.Printf("Failed to compute ping baseline for notification %d: %v", notification.Id, baselineErr)
					candidate = metricstore.PingBaselineCandidate{}
				}
				latencyEval = evaluateLatencyAnomaly(prepared, stats, candidate, windowStart, now, notification.Task.Interval)
				latencyEvaluated = true
			}
		}

		if notification.LossEnabled && (!sharedHealthLoaded || notification.WindowSeconds != notification.LatencyWindowSeconds) {
			windowStart := now.Add(-time.Duration(notification.WindowSeconds) * time.Second)
			stats, err := getPingLossStatsWithStore(context.Background(), store, notification.Client, notification.TaskId, windowStart, now)
			if err != nil {
				log.Printf("Failed to compute ping loss for notification %d: %v", notification.Id, err)
			} else {
				lossStats = stats
				lossAction = evaluatePingLossNotification(notification, stats, now)
			}
		}

		silenced := false
		if pingLossActionRequiresSend(lossAction) || pingLatencyActionRequiresSend(latencyEval.Action) {
			silenced = alertsilence.IsSilencedWithDB(db, alertsilence.KindLatency, strconv.FormatUint(uint64(notification.Id), 10), now)
		}
		lossSent := true
		if pingLossActionRequiresSend(lossAction) {
			if pingSilenceBlocksSend(silenced, lossAction) {
				lossSent = true
			} else if err := sendPingLossNotification(notification, lossStats, now, lossAction); err != nil {
				lossSent = false
				log.Printf("Failed to send ping loss notification %d: %v", notification.Id, err)
			}
		}
		latencySent := true
		if pingLatencyActionRequiresSend(latencyEval.Action) {
			if pingLatencySilenceBlocksSend(silenced, latencyEval.Action) {
				latencySent = true
			} else if err := sendPingLatencyNotification(latencyEval.Notification, latencyStats, now, latencyEval.Action); err != nil {
				latencySent = false
				log.Printf("Failed to send ping latency notification %d: %v", notification.Id, err)
			}
		}

		updates := pingHealthPersistentUpdates(notification, now, lossAction, lossSent, latencyEval, latencyEvaluated, latencySent)
		if len(updates) == 0 {
			continue
		}
		if err := db.Model(&models.PingLossNotification{}).
			Where("id = ?", notification.Id).
			Updates(pingHealthSQLUpdates(updates)).Error; err != nil {
			log.Printf("Failed to update ping loss notification %d: %v", notification.Id, err)
		}
	}
}

func getPingLossStatsWithStore(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time) (pingLossStats, error) {
	return metricstore.QueryPingLossStats(ctx, store, clientUUID, taskID, start, end)
}

func evaluatePingLossNotification(notification models.PingLossNotification, stats pingLossStats, now time.Time) pingLossNotificationAction {
	if !notification.Enable || !notification.LossEnabled || stats.Total < int64(notification.MinimumSamples) {
		return pingLossNotificationNone
	}
	if stats.LossRate() <= notification.LossThreshold {
		if !notification.AlertActive {
			return pingLossNotificationNone
		}
		if !notification.LossIncidentNotified {
			return pingLossNotificationSilentEnd
		}
		return pingLossNotificationRecovery
	}
	if notification.AlertActive && notification.LastNotified != nil && now.Before(notification.LastNotified.Add(time.Duration(notification.CooldownSeconds)*time.Second)) {
		return pingLossNotificationNone
	}
	return pingLossNotificationAlert
}

func formatPingLossMessage(notification models.PingLossNotification, stats pingLossStats, action pingLossNotificationAction) string {
	clientName := strings.TrimSpace(notification.ClientInfo.Name)
	if clientName == "" {
		clientName = notification.Client
	}
	taskName := strings.TrimSpace(notification.Task.Name)
	if taskName == "" {
		taskName = "未命名任务"
	}
	target := strings.TrimSpace(notification.Task.Target)
	if target == "" {
		target = "-"
	}
	heading := pingLossEventTitle(action)
	return fmt.Sprintf(
		"%s\n服务器：%s\n检测任务：%s\n检测目标：%s\n统计窗口：最近 %s\n丢包：%.2f%%（%d/%d）\n告警阈值：%.2f%%",
		heading,
		clientName,
		taskName,
		target,
		formatPingLossWindow(notification.WindowSeconds),
		stats.LossRate(),
		stats.Lost,
		stats.Total,
		notification.LossThreshold,
	)
}

func sendPingLatencyNotification(notification models.PingLossNotification, stats metricstore.PingHealthStats, now time.Time, action pingLatencyNotificationAction) error {
	client := notification.ClientInfo
	if client.UUID == "" {
		client.UUID = notification.Client
	}
	emoji := "⚠️"
	if action == pingLatencyNotificationRecovery {
		emoji = "✅"
	}
	return sendPingHealthEvent(models.EventMessage{
		Kind:    messageSender.KindPingLatency,
		Event:   pingLatencyEventTitle(action),
		Clients: []models.Client{client},
		Time:    now,
		Emoji:   emoji,
		Message: formatPingLatencyMessage(notification, stats, action),
	})
}

func sendPingLossNotification(notification models.PingLossNotification, stats pingLossStats, now time.Time, action pingLossNotificationAction) error {
	client := notification.ClientInfo
	if client.UUID == "" {
		client.UUID = notification.Client
	}
	emoji := "⚠️"
	if action == pingLossNotificationRecovery {
		emoji = "✅"
	}
	return sendPingHealthEvent(models.EventMessage{
		Kind:    messageSender.KindPingLoss,
		Event:   pingLossEventTitle(action),
		Clients: []models.Client{client},
		Time:    now,
		Emoji:   emoji,
		Message: formatPingLossMessage(notification, stats, action),
	})
}

func pingLossEventTitle(action pingLossNotificationAction) string {
	if action == pingLossNotificationRecovery {
		return "延迟监测告警 · 丢包恢复"
	}
	return "延迟监测告警 · 丢包异常"
}

func pingLatencyEventTitle(action pingLatencyNotificationAction) string {
	if action == pingLatencyNotificationRecovery {
		return "延迟监测告警 · 延迟恢复"
	}
	return "延迟监测告警 · 延迟异常"
}

func loadAdaptiveBaselineCandidate(
	ctx context.Context,
	store *metric.Store,
	notification models.PingLossNotification,
	now time.Time,
) (models.PingLossNotification, metricstore.PingBaselineCandidate, bool, error) {
	next := notification
	candidate := metricstore.PingBaselineCandidate{}
	if !next.AdaptiveBaselineEnabled {
		return next, candidate, false, nil
	}
	fp := models.AdaptiveBaselineFingerprint(
		next.Task.Type,
		next.Task.Target,
		next.BaselineWindowSeconds,
		next.BaselineMinimumSamples,
	)
	if next.AdaptiveBaselineFingerprint != "" && next.AdaptiveBaselineFingerprint != fp {
		resetLatencyIdentity(&next, now, fp)
	}
	if !needsAdaptiveBaselineCandidate(next) {
		return next, candidate, false, nil
	}
	start, end := metricstore.AdaptiveBaselineRange(now, next.LatencyWindowSeconds, next.BaselineWindowSeconds, next.AdaptiveBaselineStartedAt)
	if !metricstore.AdaptiveBaselineRangeValid(start, end) {
		return next, candidate, false, nil
	}
	loaded, err := queryPingBaselineCandidate(ctx, store, next.Client, next.TaskId, start, end)
	if err != nil {
		return next, candidate, true, err
	}
	return next, loaded, true, nil
}

func pingHealthPersistentUpdates(
	previous models.PingLossNotification,
	now time.Time,
	lossAction pingLossNotificationAction,
	lossSent bool,
	eval pingLatencyEvaluation,
	latencyEvaluated bool,
	latencySent bool,
) map[string]any {
	updates := map[string]any{}

	switch lossAction {
	case pingLossNotificationAlert:
		updates["alert_active"] = true
		if lossSent {
			updates["loss_incident_notified"] = true
			updates["last_notified"] = now
		} else if !previous.LossIncidentNotified {
			updates["loss_incident_notified"] = false
			updates["last_notified"] = nil
		}
	case pingLossNotificationRecovery:
		if lossSent {
			updates["alert_active"] = false
			updates["loss_incident_notified"] = false
			updates["last_notified"] = now
		}
	case pingLossNotificationSilentEnd:
		updates["alert_active"] = false
		updates["loss_incident_notified"] = false
	}

	if latencyEvaluated {
		next := eval.Notification
		if eval.Action == pingLatencyNotificationRecovery && !latencySent {
			next.LatencyAlertState = previous.LatencyAlertState
			next.LatencyActiveSince = previous.LatencyActiveSince
			next.AdaptiveBaselineStatus = previous.AdaptiveBaselineStatus
			next.AdaptiveBaselineResumeAt = previous.AdaptiveBaselineResumeAt
			next.AdaptiveBaselineMs = previous.AdaptiveBaselineMs
			next.LatencyIncidentNotified = previous.LatencyIncidentNotified
		}
		for key, value := range latencyRuntimeUpdates(previous, next) {
			updates[key] = value
		}
		if latencySent && pingLatencyActionRequiresSend(eval.Action) {
			updates["latency_last_notified"] = now
			if eval.Action != pingLatencyNotificationRecovery {
				updates["latency_incident_notified"] = true
			} else {
				updates["latency_incident_notified"] = false
			}
		} else if latencyNotificationStartsNewIncident(eval.Action) && !latencySent {
			updates["latency_last_notified"] = nil
			updates["latency_incident_notified"] = false
		} else if eval.Action == pingLatencyNotificationSilentEnd {
			updates["latency_incident_notified"] = false
		}
	}

	return updates
}

func pingLossActionRequiresSend(action pingLossNotificationAction) bool {
	return action == pingLossNotificationAlert || action == pingLossNotificationRecovery
}

func pingSilenceBlocksSend(silenced bool, action pingLossNotificationAction) bool {
	return silenced && action != pingLossNotificationRecovery
}

func pingLatencySilenceBlocksSend(silenced bool, action pingLatencyNotificationAction) bool {
	return silenced && action != pingLatencyNotificationRecovery
}

func pingLatencyActionRequiresSend(action pingLatencyNotificationAction) bool {
	switch action {
	case pingLatencyNotificationAlertHigh, pingLatencyNotificationAlertLow,
		pingLatencyNotificationPersist, pingLatencyNotificationRecovery,
		pingLatencyNotificationFlipHigh, pingLatencyNotificationFlipLow:
		return true
	default:
		return false
	}
}

func latencyNotificationStartsNewIncident(action pingLatencyNotificationAction) bool {
	switch action {
	case pingLatencyNotificationAlertHigh, pingLatencyNotificationAlertLow,
		pingLatencyNotificationFlipHigh, pingLatencyNotificationFlipLow:
		return true
	default:
		return false
	}
}

func pingHealthSQLUpdates(updates map[string]any) map[string]any {
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

func formatPingLossWindow(seconds int) string {
	if seconds%3600 == 0 {
		return fmt.Sprintf("%d 小时", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%d 分钟", seconds/60)
	}
	return fmt.Sprintf("%d 秒", seconds)
}
