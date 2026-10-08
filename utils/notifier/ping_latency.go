package notifier

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
)

type pingLatencyNotificationAction uint8

const (
	pingLatencyNotificationNone pingLatencyNotificationAction = iota
	pingLatencyNotificationAlertHigh
	pingLatencyNotificationAlertLow
	pingLatencyNotificationPersist
	pingLatencyNotificationRecovery
	pingLatencyNotificationFlipHigh
	pingLatencyNotificationFlipLow
	pingLatencyNotificationSilentEnd
)

const adaptiveBaselineEWMASlow = 0.9
const adaptiveBaselineEWMAFast = 0.1

type pingLatencyEvaluation struct {
	Notification models.PingLossNotification
	Action       pingLatencyNotificationAction
}

func latencyThresholds(notification models.PingLossNotification) (low, high float64, ok bool) {
	if notification.AdaptiveBaselineEnabled {
		if !notification.HasAdaptiveBaseline() {
			return 0, 0, false
		}
		baseline := metricstore.RoundLatencyMS(*notification.AdaptiveBaselineMs)
		low = metricstore.RoundLatencyMS(baseline * (1 - notification.AdaptiveLowerDeviationPercent/100))
		high = metricstore.RoundLatencyMS(baseline * (1 + notification.AdaptiveUpperDeviationPercent/100))
		return low, high, high > low
	}
	low = metricstore.RoundLatencyMS(notification.LowLatencyThresholdMs)
	high = metricstore.RoundLatencyMS(notification.HighLatencyThresholdMs)
	return low, high, high > low
}

func latencyCoverageTolerance(pingIntervalSeconds int) time.Duration {
	tolerance := 30 * time.Second
	if pingIntervalSeconds > 0 {
		if doubled := time.Duration(2*pingIntervalSeconds) * time.Second; doubled > tolerance {
			tolerance = doubled
		}
	}
	return tolerance
}

func latencyRequiredSuccessfulSamples(notification models.PingLossNotification, pingIntervalSeconds int) int {
	required := notification.LatencyMinimumSamples
	if required < 1 {
		required = 1
	}
	if pingIntervalSeconds <= 0 || notification.LatencyWindowSeconds <= 0 {
		return required
	}
	expected := notification.LatencyWindowSeconds / pingIntervalSeconds
	covered := int(math.Ceil(float64(expected) * 0.8))
	if covered > required {
		return covered
	}
	return required
}

func latencyWindowCovered(stats metricstore.PingHealthStats, windowStart, now time.Time, notification models.PingLossNotification, pingIntervalSeconds int) bool {
	required := latencyRequiredSuccessfulSamples(notification, pingIntervalSeconds)
	if stats.Successful < int64(required) {
		return false
	}
	if stats.FirstSuccessfulAt == nil || stats.LastSuccessfulAt == nil {
		return false
	}
	tolerance := latencyCoverageTolerance(pingIntervalSeconds)
	if stats.FirstSuccessfulAt.After(windowStart.Add(tolerance)) {
		return false
	}
	if stats.LastSuccessfulAt.Before(now.Add(-tolerance)) {
		return false
	}
	return true
}

func latencyAverageInside(stats metricstore.PingHealthStats, low, high float64) bool {
	if !stats.HasLatency {
		return false
	}
	// 0.00 ms is not a real latency, so it sits inside the band.
	if stats.AverageLatencyMS == 0 {
		return true
	}
	return stats.AverageLatencyMS > low && stats.AverageLatencyMS < high
}

func latencyBelowFloor(average, low float64) bool {
	return average > 0 && average <= low
}

func latencyAboveCeiling(average, high float64) bool {
	return average > 0 && average >= high
}

func latencyFullyRecovered(stats metricstore.PingHealthStats, windowStart, now time.Time, notification models.PingLossNotification, pingIntervalSeconds int, low, high float64) bool {
	return latencyWindowCovered(stats, windowStart, now, notification, pingIntervalSeconds) && latencyAverageInside(stats, low, high)
}

func evaluateLatencyAnomaly(
	notification models.PingLossNotification,
	stats metricstore.PingHealthStats,
	candidate metricstore.PingBaselineCandidate,
	windowStart, now time.Time,
	pingIntervalSeconds int,
) pingLatencyEvaluation {
	next := notification
	if !next.Enable || !next.LatencyEnabled {
		return pingLatencyEvaluation{Notification: next, Action: pingLatencyNotificationNone}
	}

	if next.AdaptiveBaselineEnabled {
		fp := models.AdaptiveBaselineFingerprint(next.Task.Type, next.Task.Target, next.BaselineWindowSeconds, next.BaselineMinimumSamples)
		if next.AdaptiveBaselineFingerprint != "" && next.AdaptiveBaselineFingerprint != fp {
			resetLatencyIdentity(&next, now, fp)
			return pingLatencyEvaluation{Notification: next, Action: pingLatencyNotificationNone}
		}
		next.AdaptiveBaselineFingerprint = fp
		repairStuckFrozenBaseline(&next, now)
		if !next.HasAdaptiveBaseline() || next.NormalizedAdaptiveBaselineStatus() == models.AdaptiveBaselineWarming {
			if median := metricstore.RoundLatencyMS(candidate.MedianMS); candidate.Successful >= int64(next.BaselineMinimumSamples) && median > 0 {
				next.AdaptiveBaselineMs = &median
				next.AdaptiveBaselineStatus = models.AdaptiveBaselineReady
				next.AdaptiveBaselineSampleCount = int(candidate.Successful)
				updated := now
				next.AdaptiveBaselineUpdatedAt = &updated
			} else {
				next.AdaptiveBaselineStatus = models.AdaptiveBaselineWarming
				next.AdaptiveBaselineSampleCount = int(candidate.Successful)
				return pingLatencyEvaluation{Notification: next, Action: pingLatencyNotificationNone}
			}
		}
	}

	stats.AverageLatencyMS = metricstore.RoundLatencyMS(stats.AverageLatencyMS)
	stats.MinLatencyMS = metricstore.RoundLatencyMS(stats.MinLatencyMS)
	stats.MaxLatencyMS = metricstore.RoundLatencyMS(stats.MaxLatencyMS)
	if stats.Successful < int64(next.LatencyMinimumSamples) || !stats.HasLatency || stats.AverageLatencyMS < 0 {
		return pingLatencyEvaluation{Notification: next, Action: pingLatencyNotificationNone}
	}

	low, high, ok := latencyThresholds(next)
	if !ok {
		return pingLatencyEvaluation{Notification: next, Action: pingLatencyNotificationNone}
	}

	next.LatencyLatestAverageMs = stats.AverageLatencyMS
	next.LatencySuccessfulSamples = int(stats.Successful)
	evaluated := now
	next.LatencyLastEvaluatedAt = &evaluated

	state := next.NormalizedLatencyAlertState()
	action := pingLatencyNotificationNone

	switch state {
	case models.LatencyAlertNormal:
		if latencyAboveCeiling(stats.AverageLatencyMS, high) {
			action = pingLatencyNotificationAlertHigh
			next.LatencyAlertState = models.LatencyAlertHigh
			next.LatencyIncidentNotified = false
			since := now
			next.LatencyActiveSince = &since
			if next.AdaptiveBaselineEnabled {
				next.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
				next.AdaptiveBaselineResumeAt = nil
			}
		} else if latencyBelowFloor(stats.AverageLatencyMS, low) {
			action = pingLatencyNotificationAlertLow
			next.LatencyAlertState = models.LatencyAlertLow
			next.LatencyIncidentNotified = false
			since := now
			next.LatencyActiveSince = &since
			if next.AdaptiveBaselineEnabled {
				next.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
				next.AdaptiveBaselineResumeAt = nil
			}
		}
	case models.LatencyAlertHigh:
		if latencyBelowFloor(stats.AverageLatencyMS, low) {
			action = pingLatencyNotificationFlipLow
			next.LatencyAlertState = models.LatencyAlertLow
			next.LatencyIncidentNotified = false
			since := now
			next.LatencyActiveSince = &since
			if next.AdaptiveBaselineEnabled {
				next.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
			}
		} else if latencyFullyRecovered(stats, windowStart, now, next, pingIntervalSeconds, low, high) {
			if next.LatencyIncidentNotified {
				action = pingLatencyNotificationRecovery
			} else {
				action = pingLatencyNotificationSilentEnd
			}
			applyLatencyRecovery(&next, now)
			next.LatencyIncidentNotified = false
		} else if latencyAboveCeiling(stats.AverageLatencyMS, high) && latencyCooldownElapsed(next, now) {
			action = pingLatencyNotificationPersist
			if next.LatencyLastNotified == nil {
				action = pingLatencyNotificationAlertHigh
			}
		}
	case models.LatencyAlertLow:
		if latencyAboveCeiling(stats.AverageLatencyMS, high) {
			action = pingLatencyNotificationFlipHigh
			next.LatencyAlertState = models.LatencyAlertHigh
			next.LatencyIncidentNotified = false
			since := now
			next.LatencyActiveSince = &since
			if next.AdaptiveBaselineEnabled {
				next.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
			}
		} else if latencyFullyRecovered(stats, windowStart, now, next, pingIntervalSeconds, low, high) {
			if next.LatencyIncidentNotified {
				action = pingLatencyNotificationRecovery
			} else {
				action = pingLatencyNotificationSilentEnd
			}
			applyLatencyRecovery(&next, now)
			next.LatencyIncidentNotified = false
		} else if latencyBelowFloor(stats.AverageLatencyMS, low) && latencyCooldownElapsed(next, now) {
			action = pingLatencyNotificationPersist
			if next.LatencyLastNotified == nil {
				action = pingLatencyNotificationAlertLow
			}
		}
	}

	if next.NormalizedLatencyAlertState() == models.LatencyAlertNormal {
		maybeUpdateAdaptiveBaseline(&next, stats, windowStart, now, pingIntervalSeconds, low, high)
	}

	return pingLatencyEvaluation{Notification: next, Action: action}
}

func applyLatencyRecovery(next *models.PingLossNotification, now time.Time) {
	next.LatencyAlertState = models.LatencyAlertNormal
	next.LatencyActiveSince = nil
	if !next.AdaptiveBaselineEnabled || !next.HasAdaptiveBaseline() {
		return
	}
	next.AdaptiveBaselineStatus = models.AdaptiveBaselineRecoveryHold
	resume := now.Add(time.Duration(next.LatencyWindowSeconds) * time.Second)
	next.AdaptiveBaselineResumeAt = &resume
}

func resetLatencyIdentity(next *models.PingLossNotification, now time.Time, fingerprint string) {
	if next == nil {
		return
	}
	next.LatencyAlertState = models.LatencyAlertNormal
	next.LatencyIncidentNotified = false
	next.LatencyActiveSince = nil
	next.LatencyLastNotified = nil
	next.AdaptiveBaselineMs = nil
	next.AdaptiveBaselineStatus = models.AdaptiveBaselineWarming
	next.AdaptiveBaselineSampleCount = 0
	next.AdaptiveBaselineUpdatedAt = nil
	next.AdaptiveBaselineResumeAt = nil
	next.AdaptiveBaselineFingerprint = fingerprint
	started := now.UTC()
	next.AdaptiveBaselineStartedAt = &started
}

func needsAdaptiveBaselineCandidate(notification models.PingLossNotification) bool {
	if !notification.AdaptiveBaselineEnabled {
		return false
	}
	fp := models.AdaptiveBaselineFingerprint(
		notification.Task.Type,
		notification.Task.Target,
		notification.BaselineWindowSeconds,
		notification.BaselineMinimumSamples,
	)
	if notification.AdaptiveBaselineFingerprint != "" && notification.AdaptiveBaselineFingerprint != fp {
		return true
	}
	if !notification.HasAdaptiveBaseline() {
		return true
	}
	return notification.NormalizedAdaptiveBaselineStatus() == models.AdaptiveBaselineWarming
}

func latencyCooldownElapsed(notification models.PingLossNotification, now time.Time) bool {
	if notification.LatencyLastNotified == nil {
		return true
	}
	return !now.Before(notification.LatencyLastNotified.Add(time.Duration(notification.LatencyCooldownSeconds) * time.Second))
}

func repairStuckFrozenBaseline(notification *models.PingLossNotification, now time.Time) {
	if notification == nil || !notification.AdaptiveBaselineEnabled || !notification.HasAdaptiveBaseline() {
		return
	}
	if notification.NormalizedLatencyAlertState() != models.LatencyAlertNormal {
		return
	}
	if notification.NormalizedAdaptiveBaselineStatus() != models.AdaptiveBaselineFrozen {
		return
	}
	notification.AdaptiveBaselineStatus = models.AdaptiveBaselineRecoveryHold
	window := notification.LatencyWindowSeconds
	if window < 60 {
		window = 300
	}
	resume := now.Add(time.Duration(window) * time.Second)
	notification.AdaptiveBaselineResumeAt = &resume
}

func maybeUpdateAdaptiveBaseline(
	notification *models.PingLossNotification,
	stats metricstore.PingHealthStats,
	windowStart, now time.Time,
	pingIntervalSeconds int,
	low, high float64,
) {
	if notification == nil || !notification.AdaptiveBaselineEnabled || !notification.HasAdaptiveBaseline() {
		return
	}
	status := notification.NormalizedAdaptiveBaselineStatus()
	if status == models.AdaptiveBaselineFrozen || status == models.AdaptiveBaselineWarming {
		return
	}
	if status == models.AdaptiveBaselineRecoveryHold {
		if notification.AdaptiveBaselineResumeAt != nil && now.Before(*notification.AdaptiveBaselineResumeAt) {
			return
		}
		if !latencyFullyRecovered(stats, windowStart, now, *notification, pingIntervalSeconds, low, high) {
			return
		}
		notification.AdaptiveBaselineStatus = models.AdaptiveBaselineReady
		notification.AdaptiveBaselineResumeAt = nil
	}
	if !latencyFullyRecovered(stats, windowStart, now, *notification, pingIntervalSeconds, low, high) {
		return
	}
	if notification.AdaptiveBaselineUpdatedAt != nil &&
		now.Before(notification.AdaptiveBaselineUpdatedAt.Add(time.Duration(notification.LatencyWindowSeconds)*time.Second)) {
		return
	}
	updated := metricstore.RoundLatencyMS(*notification.AdaptiveBaselineMs*adaptiveBaselineEWMASlow + stats.AverageLatencyMS*adaptiveBaselineEWMAFast)
	notification.AdaptiveBaselineMs = &updated
	updatedAt := now
	notification.AdaptiveBaselineUpdatedAt = &updatedAt
	notification.AdaptiveBaselineSampleCount = int(stats.Successful)
	if notification.AdaptiveBaselineStatus != models.AdaptiveBaselineReady {
		notification.AdaptiveBaselineStatus = models.AdaptiveBaselineReady
	}
}

func formatPingLatencyMessage(notification models.PingLossNotification, stats metricstore.PingHealthStats, action pingLatencyNotificationAction) string {
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
	low, high, _ := latencyThresholds(notification)
	heading := pingLatencyHeading(notification, action)
	mode := "固定阈值"
	if notification.AdaptiveBaselineEnabled {
		mode = "自适应基线"
	}
	lines := []string{
		heading,
		fmt.Sprintf("服务器：%s", clientName),
		fmt.Sprintf("检测任务：%s", taskName),
		fmt.Sprintf("检测目标：%s", target),
		fmt.Sprintf("判定模式：%s", mode),
		fmt.Sprintf("统计窗口：最近 %s", formatPingLossWindow(notification.LatencyWindowSeconds)),
		fmt.Sprintf("平均延迟：%.2f ms", stats.AverageLatencyMS),
	}
	if notification.AdaptiveBaselineEnabled && notification.HasAdaptiveBaseline() {
		lines = append(lines, fmt.Sprintf("冻结基线：%.2f ms", *notification.AdaptiveBaselineMs))
		if action == pingLatencyNotificationAlertHigh || action == pingLatencyNotificationPersist && notification.NormalizedLatencyAlertState() == models.LatencyAlertHigh || action == pingLatencyNotificationFlipHigh {
			lines = append(lines, fmt.Sprintf("允许上浮：%.1f%%", notification.AdaptiveUpperDeviationPercent))
			lines = append(lines, fmt.Sprintf("异常边界：>= %.2f ms", high))
		} else if action == pingLatencyNotificationAlertLow || action == pingLatencyNotificationPersist && notification.NormalizedLatencyAlertState() == models.LatencyAlertLow || action == pingLatencyNotificationFlipLow {
			lines = append(lines, fmt.Sprintf("允许下浮：%.1f%%", notification.AdaptiveLowerDeviationPercent))
			lines = append(lines, fmt.Sprintf("异常边界：<= %.2f ms", low))
		} else {
			lines = append(lines, fmt.Sprintf("允许下浮：%.1f%%", notification.AdaptiveLowerDeviationPercent))
			lines = append(lines, fmt.Sprintf("允许上浮：%.1f%%", notification.AdaptiveUpperDeviationPercent))
			lines = append(lines, fmt.Sprintf("正常范围：%.2f-%.2f ms", low, high))
		}
	} else {
		if notification.FixedBaselineMs > 0 {
			lines = append(lines, fmt.Sprintf("基线：%.2f ms", notification.FixedBaselineMs))
		}
		if action == pingLatencyNotificationRecovery {
			lines = append(lines, fmt.Sprintf("正常范围：%.2f-%.2f ms", low, high))
		} else if action == pingLatencyNotificationAlertHigh || action == pingLatencyNotificationFlipHigh || (action == pingLatencyNotificationPersist && notification.NormalizedLatencyAlertState() == models.LatencyAlertHigh) {
			lines = append(lines, fmt.Sprintf("异常边界：>= %.2f ms", high))
		} else {
			lines = append(lines, fmt.Sprintf("异常边界：<= %.2f ms", low))
		}
	}
	if action == pingLatencyNotificationRecovery {
		lines = append(lines, fmt.Sprintf("最近 %s的平均延迟已回到 %.2f-%.2f ms", formatPingLossWindow(notification.LatencyWindowSeconds), low, high))
	}
	lines = append(lines,
		fmt.Sprintf("成功样本：%d", stats.Successful),
		fmt.Sprintf("丢包率：%.2f%%", stats.LossRate()),
	)
	return strings.Join(lines, "\n")
}

func pingLatencyHeading(notification models.PingLossNotification, action pingLatencyNotificationAction) string {
	switch action {
	case pingLatencyNotificationRecovery:
		if notification.NormalizedLatencyAlertState() == models.LatencyAlertNormal {
			return "延迟监测告警 · 延迟恢复"
		}
	case pingLatencyNotificationFlipHigh:
		return "延迟监测告警 · 由过低转为过高"
	case pingLatencyNotificationFlipLow:
		return "延迟监测告警 · 由过高转为过低"
	case pingLatencyNotificationPersist:
		if notification.NormalizedLatencyAlertState() == models.LatencyAlertLow {
			return "延迟监测告警 · 延迟过低仍在持续"
		}
		return "延迟监测告警 · 延迟过高仍在持续"
	default:
		return pingLatencyEventTitle(action)
	}
	return pingLatencyEventTitle(action)
}

func latencyRuntimeUpdates(previous, next models.PingLossNotification) map[string]any {
	updates := map[string]any{
		"latency_alert_state":            next.NormalizedLatencyAlertState(),
		"latency_incident_notified":      next.LatencyIncidentNotified,
		"latency_active_since":           next.LatencyActiveSince,
		"latency_last_evaluated_at":      next.LatencyLastEvaluatedAt,
		"latency_latest_average_ms":      next.LatencyLatestAverageMs,
		"latency_successful_samples":     next.LatencySuccessfulSamples,
		"adaptive_baseline_ms":           next.AdaptiveBaselineMs,
		"adaptive_baseline_status":       next.NormalizedAdaptiveBaselineStatus(),
		"adaptive_baseline_sample_count": next.AdaptiveBaselineSampleCount,
		"adaptive_baseline_updated_at":   next.AdaptiveBaselineUpdatedAt,
		"adaptive_baseline_resume_at":    next.AdaptiveBaselineResumeAt,
		"adaptive_baseline_fingerprint":  next.AdaptiveBaselineFingerprint,
		"adaptive_baseline_started_at":   next.AdaptiveBaselineStartedAt,
	}
	_ = previous
	return updates
}
