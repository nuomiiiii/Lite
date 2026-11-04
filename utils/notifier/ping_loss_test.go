package notifier

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	messageevent "github.com/nuomiiiii/lite/database/models/messageEvent"
	"github.com/nuomiiiii/lite/pkg/metric"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPingSilenceBlocksAlertsButNotRecovery(t *testing.T) {
	assert.False(t, pingSilenceBlocksSend(false, pingLossNotificationAlert))
	assert.True(t, pingSilenceBlocksSend(true, pingLossNotificationAlert))
	assert.False(t, pingSilenceBlocksSend(true, pingLossNotificationRecovery))
	assert.True(t, pingLatencySilenceBlocksSend(true, pingLatencyNotificationAlertHigh))
	assert.True(t, pingLatencySilenceBlocksSend(true, pingLatencyNotificationPersist))
	assert.False(t, pingLatencySilenceBlocksSend(true, pingLatencyNotificationRecovery))
}

func TestEvaluatePingLossNotification(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	notification := models.PingLossNotification{
		Enable:          true,
		LossEnabled:     true,
		LossThreshold:   5,
		MinimumSamples:  10,
		CooldownSeconds: 300,
	}
	stats := pingLossStats{Total: 20, Lost: 2}
	assert.Equal(t, pingLossNotificationAlert, evaluatePingLossNotification(notification, stats, now))

	lastNotified := now.Add(-time.Minute)
	notification.LastNotified = &lastNotified
	assert.Equal(t, pingLossNotificationAlert, evaluatePingLossNotification(notification, stats, now), "a new alert must not be delayed by stale notification history")

	notification.AlertActive = true
	assert.Equal(t, pingLossNotificationNone, evaluatePingLossNotification(notification, stats, now))

	lastNotified = now.Add(-10 * time.Minute)
	assert.Equal(t, pingLossNotificationAlert, evaluatePingLossNotification(notification, stats, now))

	stats = pingLossStats{Total: 9, Lost: 9}
	assert.Equal(t, pingLossNotificationNone, evaluatePingLossNotification(notification, stats, now))

	stats = pingLossStats{Total: 20, Lost: 1}
	assert.Equal(t, pingLossNotificationSilentEnd, evaluatePingLossNotification(notification, stats, now))
	notification.LossIncidentNotified = true
	assert.Equal(t, pingLossNotificationRecovery, evaluatePingLossNotification(notification, stats, now))

	notification.AlertActive = false
	assert.Equal(t, pingLossNotificationNone, evaluatePingLossNotification(notification, stats, now))
}

func TestFormatPingLossMessageIdentifiesExactTask(t *testing.T) {
	notification := models.PingLossNotification{
		Client:          "node-a",
		ClientInfo:      models.Client{Name: "东京节点"},
		TaskId:          17,
		Task:            models.PingTask{Name: "Cloudflare DNS", Target: "1.1.1.1"},
		WindowSeconds:   60,
		LossThreshold:   5,
		MinimumSamples:  1,
		CooldownSeconds: 300,
	}
	message := formatPingLossMessage(notification, pingLossStats{Total: 20, Lost: 2}, pingLossNotificationAlert)
	for _, expected := range []string{"延迟监测告警 · 丢包异常", "东京节点", "检测任务：Cloudflare DNS", "1.1.1.1", "10.00%", "2/20"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message %q does not contain %q", message, expected)
		}
	}
	assert.NotContains(t, message, "(#17)")
	assert.Equal(t, "延迟监测告警", messageevent.PingLoss)
}

func TestFormatPingLossRecoveryMessage(t *testing.T) {
	notification := models.PingLossNotification{
		Client:        "node-a",
		ClientInfo:    models.Client{Name: "测试服务器"},
		TaskId:        117,
		Task:          models.PingTask{Name: "XX电信", Target: "example.com"},
		WindowSeconds: 60,
		LossThreshold: 5,
	}
	message := formatPingLossMessage(notification, pingLossStats{Total: 20, Lost: 1}, pingLossNotificationRecovery)
	assert.Contains(t, message, "延迟监测告警 · 丢包恢复")
	assert.Contains(t, message, "检测任务：XX电信")
	assert.NotContains(t, message, "#117")
}

func TestFormatPingLossWindow(t *testing.T) {
	assert.Equal(t, "1 分钟", formatPingLossWindow(60))
	assert.Equal(t, "2 小时", formatPingLossWindow(7200))
	assert.Equal(t, "90 秒", formatPingLossWindow(90))
}

func TestFailedLossAlertDoesNotAdvanceLastNotified(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-time.Minute)
	rule := models.PingLossNotification{
		Enable: true, LossEnabled: true, LossThreshold: 5, MinimumSamples: 10, CooldownSeconds: 1800,
		LastNotified: &stale,
	}
	stats := pingLossStats{Total: 20, Lost: 2}
	action := evaluatePingLossNotification(rule, stats, now)
	assert.Equal(t, pingLossNotificationAlert, action)

	failed := pingHealthPersistentUpdates(rule, now, action, false, pingLatencyEvaluation{Notification: rule}, false, true)
	assert.Equal(t, true, failed["alert_active"])
	value, exists := failed["last_notified"]
	assert.True(t, exists)
	assert.Nil(t, value)

	got := persistPingHealthRule(t, rule, failed)
	assert.True(t, got.AlertActive)
	assert.Nil(t, got.LastNotified)
	retry := evaluatePingLossNotification(got, stats, now.Add(15*time.Second))
	assert.Equal(t, pingLossNotificationAlert, retry)
}

func TestFailedLossRecoveryRetries(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	rule := models.PingLossNotification{
		Enable: true, LossEnabled: true, LossThreshold: 5, MinimumSamples: 10, CooldownSeconds: 300, AlertActive: true, LossIncidentNotified: true,
	}
	stats := pingLossStats{Total: 20, Lost: 1}
	action := evaluatePingLossNotification(rule, stats, now)
	assert.Equal(t, pingLossNotificationRecovery, action)
	failed := pingHealthPersistentUpdates(rule, now, action, false, pingLatencyEvaluation{Notification: rule}, false, true)
	_, exists := failed["alert_active"]
	assert.False(t, exists)
	_, exists = failed["last_notified"]
	assert.False(t, exists)
	retry := evaluatePingLossNotification(rule, stats, now.Add(15*time.Second))
	assert.Equal(t, pingLossNotificationRecovery, retry)
}

func TestMixedLossAndLatencySendResultsCommitIndependently(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LossEnabled = true
	rule.LossThreshold = 5
	rule.MinimumSamples = 10
	rule.CooldownSeconds = 300
	eval := evaluateLatencyAnomaly(rule, coveredLatencyStats(130, 120, 140, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, eval.Action)

	lossOK := pingHealthPersistentUpdates(rule, now, pingLossNotificationAlert, true, eval, true, false)
	assert.Equal(t, now, lossOK["last_notified"])
	assert.Equal(t, true, lossOK["alert_active"])
	value, exists := lossOK["latency_last_notified"]
	assert.True(t, exists)
	assert.Nil(t, value)
	assert.Equal(t, models.LatencyAlertHigh, lossOK["latency_alert_state"])

	latencyOK := pingHealthPersistentUpdates(rule, now, pingLossNotificationAlert, false, eval, true, true)
	value, exists = latencyOK["last_notified"]
	assert.True(t, exists)
	assert.Nil(t, value)
	assert.Equal(t, true, latencyOK["alert_active"])
	assert.Equal(t, now, latencyOK["latency_last_notified"])
}

func TestLoadAdaptiveBaselineCandidateSkipsReadyFrozenAndHold(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	calls := 0
	previous := queryPingBaselineCandidate
	queryPingBaselineCandidate = func(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time) (metricstore.PingBaselineCandidate, error) {
		calls++
		return metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, nil
	}
	t.Cleanup(func() { queryPingBaselineCandidate = previous })

	ready := adaptiveLatencyRule(100)
	ready.AdaptiveBaselineFingerprint = models.AdaptiveBaselineFingerprint("icmp", "api.example.com", 86400, 30)
	_, _, queried, err := loadAdaptiveBaselineCandidate(context.Background(), nil, ready, now)
	require.NoError(t, err)
	assert.False(t, queried)

	frozen := ready
	frozen.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	_, _, queried, err = loadAdaptiveBaselineCandidate(context.Background(), nil, frozen, now)
	require.NoError(t, err)
	assert.False(t, queried)

	hold := ready
	hold.AdaptiveBaselineStatus = models.AdaptiveBaselineRecoveryHold
	_, _, queried, err = loadAdaptiveBaselineCandidate(context.Background(), nil, hold, now)
	require.NoError(t, err)
	assert.False(t, queried)
	assert.Equal(t, 0, calls)

	warming := ready
	warming.AdaptiveBaselineMs = nil
	warming.AdaptiveBaselineStatus = models.AdaptiveBaselineWarming
	_, _, queried, err = loadAdaptiveBaselineCandidate(context.Background(), nil, warming, now)
	require.NoError(t, err)
	assert.True(t, queried)

	missing := ready
	missing.AdaptiveBaselineMs = nil
	_, _, queried, err = loadAdaptiveBaselineCandidate(context.Background(), nil, missing, now)
	require.NoError(t, err)
	assert.True(t, queried)

	mismatch := ready
	mismatch.AdaptiveBaselineFingerprint = models.AdaptiveBaselineFingerprint("icmp", "old.example.com", 86400, 30)
	prepared, _, queried, err := loadAdaptiveBaselineCandidate(context.Background(), nil, mismatch, now)
	require.NoError(t, err)
	assert.False(t, queried, "identity reset starts learning now, so the old history range is empty")
	assert.Equal(t, models.AdaptiveBaselineWarming, prepared.NormalizedAdaptiveBaselineStatus())
	assert.Equal(t, 2, calls)
}

func TestFailedHighLatencyAlertClearsStaleLastNotified(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	stale := now.Add(-time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyCooldownSeconds = 1800
	rule.LatencyLastNotified = &stale
	stats := coveredLatencyStats(130, 120, 140, 18, windowStart, now)
	eval := evaluateLatencyAnomaly(rule, stats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, eval.Action)

	failed := pingHealthPersistentUpdates(rule, now, pingLossNotificationNone, true, eval, true, false)
	value, exists := failed["latency_last_notified"]
	assert.True(t, exists)
	assert.Nil(t, value)
	got := persistPingHealthRule(t, rule, failed)
	assert.Equal(t, models.LatencyAlertHigh, got.NormalizedLatencyAlertState())
	assert.Equal(t, models.AdaptiveBaselineFrozen, got.NormalizedAdaptiveBaselineStatus())
	assert.Nil(t, got.LatencyLastNotified)
	require.True(t, got.HasAdaptiveBaseline())
	assert.Equal(t, 100.0, *got.AdaptiveBaselineMs)

	later := now.Add(15 * time.Second)
	retry := evaluateLatencyAnomaly(got, stats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, retry.Action)
	assert.Equal(t, 100.0, *retry.Notification.AdaptiveBaselineMs)
}

func TestFailedLowLatencyAlertClearsStaleLastNotified(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	stale := now.Add(-time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyCooldownSeconds = 1800
	rule.LatencyLastNotified = &stale
	stats := coveredLatencyStats(70, 60, 75, 18, windowStart, now)
	eval := evaluateLatencyAnomaly(rule, stats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertLow, eval.Action)

	failed := pingHealthPersistentUpdates(rule, now, pingLossNotificationNone, true, eval, true, false)
	got := persistPingHealthRule(t, rule, failed)
	assert.Equal(t, models.LatencyAlertLow, got.NormalizedLatencyAlertState())
	assert.Nil(t, got.LatencyLastNotified)
	later := now.Add(15 * time.Second)
	retry := evaluateLatencyAnomaly(got, stats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationAlertLow, retry.Action)
}

func TestFailedLatencyFlipClearsStaleLastNotified(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	stale := now.Add(-time.Minute)
	high := adaptiveLatencyRule(100)
	high.LatencyCooldownSeconds = 1800
	high.LatencyAlertState = models.LatencyAlertHigh
	high.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	high.LatencyLastNotified = &stale
	lowStats := coveredLatencyStats(70, 60, 75, 18, windowStart, now)
	flipLow := evaluateLatencyAnomaly(high, lowStats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationFlipLow, flipLow.Action)
	gotLow := persistPingHealthRule(t, high, pingHealthPersistentUpdates(high, now, pingLossNotificationNone, true, flipLow, true, false))
	assert.Equal(t, models.LatencyAlertLow, gotLow.NormalizedLatencyAlertState())
	assert.Nil(t, gotLow.LatencyLastNotified)
	later := now.Add(15 * time.Second)
	retryLow := evaluateLatencyAnomaly(gotLow, lowStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationAlertLow, retryLow.Action)

	low := adaptiveLatencyRule(100)
	low.LatencyCooldownSeconds = 1800
	low.LatencyAlertState = models.LatencyAlertLow
	low.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	low.LatencyLastNotified = &stale
	highStats := coveredLatencyStats(130, 120, 140, 18, windowStart, now)
	flipHigh := evaluateLatencyAnomaly(low, highStats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationFlipHigh, flipHigh.Action)
	gotHigh := persistPingHealthRule(t, low, pingHealthPersistentUpdates(low, now, pingLossNotificationNone, true, flipHigh, true, false))
	assert.Equal(t, models.LatencyAlertHigh, gotHigh.NormalizedLatencyAlertState())
	assert.Nil(t, gotHigh.LatencyLastNotified)
	retryHigh := evaluateLatencyAnomaly(gotHigh, highStats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, retryHigh.Action)
}

func persistPingHealthRule(t *testing.T, rule models.PingLossNotification, updates map[string]any) models.PingLossNotification {
	t.Helper()
	db, id := seedPingHealthRule(t, rule)
	if len(updates) == 0 {
		var got models.PingLossNotification
		require.NoError(t, db.First(&got, id).Error)
		got.Task = rule.Task
		return got
	}
	got := applyPingHealthUpdates(t, db, id, updates)
	got.Task = rule.Task
	return got
}

func seedPingHealthRule(t *testing.T, rule models.PingLossNotification) (db *gorm.DB, id uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ping-health-runtime.db")+"?_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&models.PingLossNotification{}))
	copy := rule
	copy.Id = 0
	if copy.Client == "" {
		copy.Client = "client-a"
	}
	if copy.WindowSeconds == 0 {
		copy.WindowSeconds = 60
	}
	if copy.LossThreshold == 0 {
		copy.LossThreshold = 5
	}
	if copy.MinimumSamples == 0 {
		copy.MinimumSamples = 1
	}
	if copy.CooldownSeconds == 0 {
		copy.CooldownSeconds = 300
	}
	require.NoError(t, db.Create(&copy).Error)
	return db, copy.Id
}

func applyPingHealthUpdates(t *testing.T, db *gorm.DB, id uint, updates map[string]any) models.PingLossNotification {
	t.Helper()
	require.NotEmpty(t, updates)
	require.NoError(t, db.Model(&models.PingLossNotification{}).Where("id = ?", id).Updates(pingHealthSQLUpdates(updates)).Error)
	var got models.PingLossNotification
	require.NoError(t, db.First(&got, id).Error)
	return got
}
