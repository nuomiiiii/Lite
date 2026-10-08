package notifier

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func coveredLatencyStats(avg, min, max float64, successful int64, windowStart, now time.Time) metricstore.PingHealthStats {
	first := windowStart
	last := now
	return metricstore.PingHealthStats{
		Total:             successful,
		Successful:        successful,
		HasLatency:        true,
		AverageLatencyMS:  avg,
		MinLatencyMS:      min,
		MaxLatencyMS:      max,
		FirstSuccessfulAt: &first,
		LastSuccessfulAt:  &last,
	}
}

func fixedLatencyRule() models.PingLossNotification {
	return models.PingLossNotification{
		Enable:                 true,
		LatencyEnabled:         true,
		LatencyWindowSeconds:   300,
		LatencyMinimumSamples:  3,
		LatencyCooldownSeconds: 1800,
		FixedBaselineMs:        150,
		LowLatencyThresholdMs:  120,
		HighLatencyThresholdMs: 180,
		LatencyAlertState:      models.LatencyAlertNormal,
		AdaptiveBaselineStatus: models.AdaptiveBaselineReady,
	}
}

func adaptiveLatencyRule(baseline float64) models.PingLossNotification {
	value := baseline
	return models.PingLossNotification{
		Enable:                        true,
		LatencyEnabled:                true,
		AdaptiveBaselineEnabled:       true,
		LatencyWindowSeconds:          300,
		LatencyMinimumSamples:         3,
		LatencyCooldownSeconds:        1800,
		AdaptiveLowerDeviationPercent: 20,
		AdaptiveUpperDeviationPercent: 20,
		BaselineWindowSeconds:         86400,
		BaselineMinimumSamples:        30,
		AdaptiveBaselineMs:            &value,
		AdaptiveBaselineStatus:        models.AdaptiveBaselineReady,
		LatencyAlertState:             models.LatencyAlertNormal,
		Task:                          models.PingTask{Type: "icmp", Target: "api.example.com", Interval: 10},
	}
}

func TestEvaluateFixedLatencyThresholds(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := fixedLatencyRule()

	high := evaluateLatencyAnomaly(rule, coveredLatencyStats(180, 150, 205, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, high.Action)
	assert.Equal(t, models.LatencyAlertHigh, high.Notification.LatencyAlertState)

	low := evaluateLatencyAnomaly(rule, coveredLatencyStats(120, 105, 150, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertLow, low.Action)

	zero := evaluateLatencyAnomaly(rule, coveredLatencyStats(0, 0, 0, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, zero.Action)
	assert.Equal(t, models.LatencyAlertNormal, zero.Notification.LatencyAlertState)

	subCentisecond := fixedLatencyRule()
	subCentisecond.LowLatencyThresholdMs = 0.01
	subCentisecond.HighLatencyThresholdMs = 10
	roundedOff := evaluateLatencyAnomaly(subCentisecond, coveredLatencyStats(0.004, 0.004, 0.004, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, roundedOff.Action)
	assert.Equal(t, 0.0, roundedOff.Notification.LatencyLatestAverageMs)

	rule.LatencyAlertState = models.LatencyAlertLow
	rule.LatencyIncidentNotified = true
	cleared := evaluateLatencyAnomaly(rule, coveredLatencyStats(0, 0, 0, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationRecovery, cleared.Action)
	assert.Equal(t, models.LatencyAlertNormal, cleared.Notification.LatencyAlertState)
	rule.LatencyAlertState = models.LatencyAlertNormal
	rule.LatencyIncidentNotified = false

	spike := evaluateLatencyAnomaly(rule, coveredLatencyStats(160, 145, 210, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, spike.Action)

	short := evaluateLatencyAnomaly(rule, coveredLatencyStats(200, 200, 200, 2, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, short.Action)
}

func TestEvaluateFixedLatencyRecoveryRequiresFullWindow(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := fixedLatencyRule()
	rule.LatencyAlertState = models.LatencyAlertHigh
	rule.LatencyIncidentNotified = true
	recent := now.Add(-time.Minute)
	notified := now.Add(-time.Hour)
	rule.LatencyLastNotified = &recent
	stillHigh := coveredLatencyStats(180, 170, 190, 18, windowStart, now)
	atBoundary := evaluateLatencyAnomaly(rule, stillHigh, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, atBoundary.Action, "an average still on the boundary must not recover")
	assert.Equal(t, models.LatencyAlertHigh, atBoundary.Notification.LatencyAlertState)

	inside := coveredLatencyStats(150, 100, 210, 18, windowStart, now)
	recovered := evaluateLatencyAnomaly(rule, inside, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationRecovery, recovered.Action)
	assert.Equal(t, models.LatencyAlertNormal, recovered.Notification.LatencyAlertState)

	lateFirst := inside
	late := windowStart.Add(2 * time.Minute)
	lateFirst.FirstSuccessfulAt = &late
	uncovered := evaluateLatencyAnomaly(rule, lateFirst, metricstore.PingBaselineCandidate{}, windowStart, now, 10)
	assert.Equal(t, pingLatencyNotificationNone, uncovered.Action)

	rule.LatencyLastNotified = &notified
	persist := evaluateLatencyAnomaly(rule, coveredLatencyStats(200, 190, 210, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationPersist, persist.Action)
}

func TestEvaluateAdaptiveClearsWhenAverageReturns(t *testing.T) {
	now := time.Date(2026, 9, 30, 13, 2, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(0.5)
	rule.AdaptiveUpperDeviationPercent = 25
	rule.AdaptiveLowerDeviationPercent = 25
	rule.LatencyAlertState = models.LatencyAlertHigh
	rule.LatencyIncidentNotified = true
	rule.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	notified := now.Add(-31 * time.Minute)
	rule.LatencyLastNotified = &notified

	back := evaluateLatencyAnomaly(rule, coveredLatencyStats(0.5, 0.2, 1, 60, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 5)
	assert.Equal(t, pingLatencyNotificationRecovery, back.Action)
	assert.Equal(t, models.LatencyAlertNormal, back.Notification.LatencyAlertState)

	late := windowStart.Add(3 * time.Minute)
	uncoveredStats := coveredLatencyStats(0.5, 0.2, 1, 60, windowStart, now)
	uncoveredStats.FirstSuccessfulAt = &late
	uncovered := evaluateLatencyAnomaly(rule, uncoveredStats, metricstore.PingBaselineCandidate{}, windowStart, now, 5)
	assert.Equal(t, pingLatencyNotificationNone, uncovered.Action)
	assert.Equal(t, models.LatencyAlertHigh, uncovered.Notification.LatencyAlertState)
}

func TestEvaluateAdaptiveBaselineWarmingAndThresholds(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(0)
	rule.AdaptiveBaselineMs = nil
	rule.AdaptiveBaselineStatus = models.AdaptiveBaselineWarming

	warming := evaluateLatencyAnomaly(rule, coveredLatencyStats(150, 140, 160, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 18, MedianMS: 100}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, warming.Action)
	assert.Equal(t, models.AdaptiveBaselineWarming, warming.Notification.NormalizedAdaptiveBaselineStatus())
	assert.False(t, warming.Notification.HasAdaptiveBaseline())

	ready := evaluateLatencyAnomaly(rule, coveredLatencyStats(100, 90, 110, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 30, MedianMS: 100}, windowStart, now, 0)
	require.True(t, ready.Notification.HasAdaptiveBaseline())
	assert.Equal(t, 100.0, *ready.Notification.AdaptiveBaselineMs)
	low, high, ok := latencyThresholds(ready.Notification)
	require.True(t, ok)
	assert.Equal(t, 80.0, low)
	assert.Equal(t, 120.0, high)
}

func TestEvaluateAdaptiveTriggerBeforeEWMAAndFreeze(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	updated := now.Add(-10 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.AdaptiveBaselineUpdatedAt = &updated

	high := evaluateLatencyAnomaly(rule, coveredLatencyStats(120, 110, 130, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, high.Action)
	require.True(t, high.Notification.HasAdaptiveBaseline())
	assert.Equal(t, 100.0, *high.Notification.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineFrozen, high.Notification.NormalizedAdaptiveBaselineStatus())

	low := evaluateLatencyAnomaly(rule, coveredLatencyStats(80, 70, 90, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertLow, low.Action)

	inside := evaluateLatencyAnomaly(rule, coveredLatencyStats(100, 81, 119, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, inside.Action)
	require.True(t, inside.Notification.HasAdaptiveBaseline())
	assert.InDelta(t, 100.0, *inside.Notification.AdaptiveBaselineMs, 0.0001, "EWMA must not run when the previous update is still inside the window")
}

func TestEvaluateAdaptiveEWMAAndHold(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	updated := now.Add(-6 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.AdaptiveBaselineUpdatedAt = &updated

	updatedBaseline := evaluateLatencyAnomaly(rule, coveredLatencyStats(110, 90, 115, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, updatedBaseline.Action)
	require.True(t, updatedBaseline.Notification.HasAdaptiveBaseline())
	assert.InDelta(t, 101.0, *updatedBaseline.Notification.AdaptiveBaselineMs, 0.0001)

	rule.LatencyAlertState = models.LatencyAlertHigh
	rule.LatencyIncidentNotified = true
	rule.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	frozenNow := now.Add(3 * time.Hour)
	frozenWindow := frozenNow.Add(-5 * time.Minute)
	stillFrozen := evaluateLatencyAnomaly(rule, coveredLatencyStats(130, 120, 140, 5, frozenWindow, frozenNow), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, frozenWindow, frozenNow, 0)
	assert.Equal(t, models.LatencyAlertHigh, stillFrozen.Notification.LatencyAlertState)
	require.True(t, stillFrozen.Notification.HasAdaptiveBaseline())
	assert.Equal(t, 100.0, *stillFrozen.Notification.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineFrozen, stillFrozen.Notification.NormalizedAdaptiveBaselineStatus())

	recovered := evaluateLatencyAnomaly(rule, coveredLatencyStats(100, 81, 119, 18, frozenWindow, frozenNow), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, frozenWindow, frozenNow, 0)
	assert.Equal(t, pingLatencyNotificationRecovery, recovered.Action)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, recovered.Notification.NormalizedAdaptiveBaselineStatus())
	require.NotNil(t, recovered.Notification.AdaptiveBaselineResumeAt)
	assert.Equal(t, 100.0, *recovered.Notification.AdaptiveBaselineMs)

	holdNow := recovered.Notification.AdaptiveBaselineResumeAt.Add(-time.Minute)
	holdWindow := holdNow.Add(-5 * time.Minute)
	holdRule := recovered.Notification
	holdRule.LatencyLastNotified = recovered.Notification.LatencyLastNotified
	duringHold := evaluateLatencyAnomaly(holdRule, coveredLatencyStats(110, 90, 115, 18, holdWindow, holdNow), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, holdWindow, holdNow, 0)
	assert.Equal(t, 100.0, *duringHold.Notification.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, duringHold.Notification.NormalizedAdaptiveBaselineStatus())

	readyNow := recovered.Notification.AdaptiveBaselineResumeAt.Add(time.Minute)
	readyWindow := readyNow.Add(-5 * time.Minute)
	afterHold := evaluateLatencyAnomaly(holdRule, coveredLatencyStats(110, 90, 115, 18, readyWindow, readyNow), metricstore.PingBaselineCandidate{Successful: 40, MedianMS: 100}, readyWindow, readyNow, 0)
	assert.Equal(t, models.AdaptiveBaselineReady, afterHold.Notification.NormalizedAdaptiveBaselineStatus())
	assert.InDelta(t, 101.0, *afterHold.Notification.AdaptiveBaselineMs, 0.0001)
}

func TestEvaluateAdaptiveFingerprintReset(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyAlertState = models.LatencyAlertHigh
	rule.LatencyIncidentNotified = true
	notified := now.Add(-time.Hour)
	rule.LatencyLastNotified = &notified
	rule.AdaptiveBaselineFingerprint = models.AdaptiveBaselineFingerprint("icmp", "old.example.com", 86400, 30)
	rule.Task.Target = "new.example.com"
	reset := evaluateLatencyAnomaly(rule, coveredLatencyStats(150, 140, 160, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 30, MedianMS: 150}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, reset.Action)
	assert.False(t, reset.Notification.HasAdaptiveBaseline())
	assert.Equal(t, models.AdaptiveBaselineWarming, reset.Notification.NormalizedAdaptiveBaselineStatus())
	assert.Equal(t, models.LatencyAlertNormal, reset.Notification.NormalizedLatencyAlertState())
	assert.Nil(t, reset.Notification.LatencyLastNotified)
	assert.Nil(t, reset.Notification.LatencyActiveSince)
	require.NotNil(t, reset.Notification.AdaptiveBaselineStartedAt)
	assert.Equal(t, now.UTC(), reset.Notification.AdaptiveBaselineStartedAt.UTC())
	assert.Equal(t, models.AdaptiveBaselineFingerprint("icmp", "new.example.com", 86400, 30), reset.Notification.AdaptiveBaselineFingerprint)
}

func TestEvaluateAdaptiveIndependentBaselines(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	tokyo := adaptiveLatencyRule(0)
	tokyo.AdaptiveBaselineMs = nil
	tokyo.Client = "tokyo"
	tokyo.Task.Target = "tokyo.example.com"
	osaka := adaptiveLatencyRule(0)
	osaka.AdaptiveBaselineMs = nil
	osaka.Client = "osaka"
	osaka.Task.Target = "osaka.example.com"

	tokyoReady := evaluateLatencyAnomaly(tokyo, coveredLatencyStats(80, 70, 90, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 30, MedianMS: 80}, windowStart, now, 0)
	osakaReady := evaluateLatencyAnomaly(osaka, coveredLatencyStats(40, 30, 50, 18, windowStart, now), metricstore.PingBaselineCandidate{Successful: 30, MedianMS: 40}, windowStart, now, 0)
	require.True(t, tokyoReady.Notification.HasAdaptiveBaseline())
	require.True(t, osakaReady.Notification.HasAdaptiveBaseline())
	assert.Equal(t, 80.0, *tokyoReady.Notification.AdaptiveBaselineMs)
	assert.Equal(t, 40.0, *osakaReady.Notification.AdaptiveBaselineMs)
}

func TestFormatPingLatencyMessage(t *testing.T) {
	baseline := 100.0
	notification := adaptiveLatencyRule(100)
	notification.ClientInfo = models.Client{Name: "东京节点"}
	notification.Task.Name = "业务 API"
	notification.Task.Target = "api.example.com"
	notification.AdaptiveBaselineMs = &baseline
	stats := coveredLatencyStats(126.4, 120, 130, 28, time.Now(), time.Now())
	stats.Total = 30
	stats.Lost = 2
	message := formatPingLatencyMessage(notification, stats, pingLatencyNotificationAlertHigh)
	for _, expected := range []string{"延迟监测告警 · 延迟异常", "东京节点", "业务 API", "自适应基线", "126.40 ms", "100.00 ms", "20.0%", ">= 120.00 ms"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message %q does not contain %q", message, expected)
		}
	}
	assert.NotContains(t, message, "90.00 ms")
	assert.NotContains(t, message, "110.00 ms")

	fixed := fixedLatencyRule()
	fixed.ClientInfo = models.Client{Name: "东京节点"}
	fixed.Task.Name = "业务 API"
	fixed.Task.Target = "api.example.com"
	fixedMessage := formatPingLatencyMessage(fixed, stats, pingLatencyNotificationAlertHigh)
	for _, expected := range []string{"固定阈值", "基线：150.00 ms", ">= 180.00 ms"} {
		if !strings.Contains(fixedMessage, expected) {
			t.Fatalf("fixed message %q does not contain %q", fixedMessage, expected)
		}
	}
}

func TestLatencyThresholdsDoNotInventHysteresis(t *testing.T) {
	low, high, ok := latencyThresholds(adaptiveLatencyRule(100))
	require.True(t, ok)
	assert.Equal(t, 80.0, low)
	assert.Equal(t, 120.0, high)
	assert.True(t, math.Abs((low+high)/2-100) < 0.0001)
}

func TestNeedsAdaptiveBaselineCandidate(t *testing.T) {
	ready := adaptiveLatencyRule(100)
	ready.AdaptiveBaselineFingerprint = models.AdaptiveBaselineFingerprint("icmp", "api.example.com", 86400, 30)
	assert.False(t, needsAdaptiveBaselineCandidate(ready))

	frozen := ready
	frozen.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	assert.False(t, needsAdaptiveBaselineCandidate(frozen))

	hold := ready
	hold.AdaptiveBaselineStatus = models.AdaptiveBaselineRecoveryHold
	assert.False(t, needsAdaptiveBaselineCandidate(hold))

	warming := ready
	warming.AdaptiveBaselineMs = nil
	warming.AdaptiveBaselineStatus = models.AdaptiveBaselineWarming
	assert.True(t, needsAdaptiveBaselineCandidate(warming))

	missing := ready
	missing.AdaptiveBaselineMs = nil
	assert.True(t, needsAdaptiveBaselineCandidate(missing))

	mismatch := ready
	mismatch.AdaptiveBaselineFingerprint = models.AdaptiveBaselineFingerprint("icmp", "old.example.com", 86400, 30)
	assert.True(t, needsAdaptiveBaselineCandidate(mismatch))
}

func TestFailedFirstLatencyAlertRetriesWithoutLearning(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	stale := now.Add(-time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyCooldownSeconds = 1800
	rule.LatencyLastNotified = &stale
	stats := coveredLatencyStats(130, 120, 140, 18, windowStart, now)
	eval := evaluateLatencyAnomaly(rule, stats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, eval.Action)
	assert.Equal(t, models.AdaptiveBaselineFrozen, eval.Notification.NormalizedAdaptiveBaselineStatus())
	assert.Equal(t, 100.0, *eval.Notification.AdaptiveBaselineMs)

	failed := pingHealthPersistentUpdates(rule, now, pingLossNotificationNone, true, eval, true, false)
	assert.Equal(t, models.LatencyAlertHigh, failed["latency_alert_state"])
	assert.Equal(t, models.AdaptiveBaselineFrozen, failed["adaptive_baseline_status"])
	value, exists := failed["latency_last_notified"]
	assert.True(t, exists)
	assert.Nil(t, value)

	got := persistPingHealthRule(t, rule, failed)
	later := now.Add(15 * time.Second)
	retry := evaluateLatencyAnomaly(got, stats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, retry.Action)
	assert.Equal(t, 100.0, *retry.Notification.AdaptiveBaselineMs)
}

func TestFailedLowLatencyAlertRetriesWithoutLearning(t *testing.T) {
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
	value, exists := failed["latency_last_notified"]
	assert.True(t, exists)
	assert.Nil(t, value)
	got := persistPingHealthRule(t, rule, failed)
	assert.Equal(t, models.LatencyAlertLow, got.NormalizedLatencyAlertState())
	later := now.Add(15 * time.Second)
	retry := evaluateLatencyAnomaly(got, stats, metricstore.PingBaselineCandidate{}, later.Add(-5*time.Minute), later, 0)
	assert.Equal(t, pingLatencyNotificationAlertLow, retry.Action)
}

func TestFailedLatencyRecoveryKeepsFrozenState(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyAlertState = models.LatencyAlertHigh
	rule.LatencyIncidentNotified = true
	rule.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	since := now.Add(-time.Hour)
	rule.LatencyActiveSince = &since
	notified := now.Add(-2 * time.Hour)
	rule.LatencyLastNotified = &notified

	eval := evaluateLatencyAnomaly(rule, coveredLatencyStats(100, 81, 119, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationRecovery, eval.Action)
	assert.Equal(t, models.LatencyAlertNormal, eval.Notification.NormalizedLatencyAlertState())

	failed := pingHealthPersistentUpdates(rule, now, pingLossNotificationNone, true, eval, true, false)
	assert.Equal(t, models.LatencyAlertHigh, failed["latency_alert_state"])
	assert.Equal(t, models.AdaptiveBaselineFrozen, failed["adaptive_baseline_status"])
	_, exists := failed["latency_last_notified"]
	assert.False(t, exists)

	retry := evaluateLatencyAnomaly(rule, coveredLatencyStats(100, 81, 119, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationRecovery, retry.Action)
}

func TestFailedPersistDoesNotRestartCooldown(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyAlertState = models.LatencyAlertHigh
	rule.LatencyIncidentNotified = true
	rule.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	previous := now.Add(-2 * time.Hour)
	rule.LatencyLastNotified = &previous
	eval := evaluateLatencyAnomaly(rule, coveredLatencyStats(130, 120, 140, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationPersist, eval.Action)
	failed := pingHealthPersistentUpdates(rule, now, pingLossNotificationNone, true, eval, true, false)
	_, exists := failed["latency_last_notified"]
	assert.False(t, exists)
	assert.Equal(t, models.AdaptiveBaselineFrozen, failed["adaptive_baseline_status"])
	got := persistPingHealthRule(t, rule, failed)
	require.NotNil(t, got.LatencyLastNotified)
	assert.Equal(t, previous.UTC(), got.LatencyLastNotified.UTC())
}

func TestConfigEndedHoldDoesNotLearnWidenedWindow(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	resume := now.Add(5 * time.Minute)
	rule := widenedAdaptiveRule(resume)
	stats := coveredLatencyStats(130, 121, 139, 18, windowStart, now)
	eval := evaluateLatencyAnomaly(rule, stats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, eval.Action)
	require.NotNil(t, eval.Notification.AdaptiveBaselineMs)
	assert.Equal(t, 100.0, *eval.Notification.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, eval.Notification.NormalizedAdaptiveBaselineStatus())
}

func TestRecoveryHoldLearnsOnlyAfterFullCleanWindow(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	resume := now.Add(5 * time.Minute)
	rule := widenedAdaptiveRule(resume)
	earlyStats := coveredLatencyStats(130, 121, 139, 18, now.Add(-5*time.Minute), now)
	early := evaluateLatencyAnomaly(rule, earlyStats, metricstore.PingBaselineCandidate{}, now.Add(-5*time.Minute), now, 0)
	assert.Equal(t, 100.0, *early.Notification.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, early.Notification.NormalizedAdaptiveBaselineStatus())

	later := resume.Add(time.Minute)
	laterWindow := later.Add(-5 * time.Minute)
	uncoveredStats := coveredLatencyStats(130, 121, 139, 18, laterWindow, later)
	lateFirst := laterWindow.Add(2 * time.Minute)
	uncoveredStats.FirstSuccessfulAt = &lateFirst
	uncovered := evaluateLatencyAnomaly(rule, uncoveredStats, metricstore.PingBaselineCandidate{}, laterWindow, later, 10)
	assert.Equal(t, 100.0, *uncovered.Notification.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, uncovered.Notification.NormalizedAdaptiveBaselineStatus())

	boundary := coveredLatencyStats(130, 80, 139.9, 18, laterWindow, later)
	closed := evaluateLatencyAnomaly(rule, boundary, metricstore.PingBaselineCandidate{}, laterWindow, later, 0)
	assert.Equal(t, 100.0, *closed.Notification.AdaptiveBaselineMs)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, closed.Notification.NormalizedAdaptiveBaselineStatus())

	readyStats := coveredLatencyStats(130, 80.1, 139.9, 18, laterWindow, later)
	ready := evaluateLatencyAnomaly(rule, readyStats, metricstore.PingBaselineCandidate{}, laterWindow, later, 0)
	assert.Equal(t, pingLatencyNotificationNone, ready.Action)
	assert.Equal(t, models.AdaptiveBaselineReady, ready.Notification.NormalizedAdaptiveBaselineStatus())
	assert.Nil(t, ready.Notification.AdaptiveBaselineResumeAt)
	require.NotNil(t, ready.Notification.AdaptiveBaselineMs)
	assert.InDelta(t, 103.0, *ready.Notification.AdaptiveBaselineMs, 0.0001)

	persisted := persistPingHealthRule(t, rule, pingHealthPersistentUpdates(rule, later, pingLossNotificationNone, true, ready, true, true))
	require.NotNil(t, persisted.AdaptiveBaselineMs)
	assert.InDelta(t, 103.0, *persisted.AdaptiveBaselineMs, 0.0001)
	assert.Equal(t, models.AdaptiveBaselineReady, persisted.NormalizedAdaptiveBaselineStatus())
}

func TestStuckFrozenBaselineBecomesRecoveryHold(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(100)
	rule.LatencyAlertState = models.LatencyAlertNormal
	rule.AdaptiveBaselineStatus = models.AdaptiveBaselineFrozen
	stats := coveredLatencyStats(110, 90, 115, 18, windowStart, now)
	eval := evaluateLatencyAnomaly(rule, stats, metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, eval.Action)
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, eval.Notification.NormalizedAdaptiveBaselineStatus())
	require.NotNil(t, eval.Notification.AdaptiveBaselineMs)
	assert.Equal(t, 100.0, *eval.Notification.AdaptiveBaselineMs)
	require.NotNil(t, eval.Notification.AdaptiveBaselineResumeAt)
	assert.Equal(t, now.Add(5*time.Minute).UTC(), eval.Notification.AdaptiveBaselineResumeAt.UTC())

	got := persistPingHealthRule(t, rule, pingHealthPersistentUpdates(rule, now, pingLossNotificationNone, true, eval, true, true))
	assert.Equal(t, models.LatencyAlertNormal, got.NormalizedLatencyAlertState())
	assert.Equal(t, models.AdaptiveBaselineRecoveryHold, got.NormalizedAdaptiveBaselineStatus())
	require.NotNil(t, got.AdaptiveBaselineMs)
	assert.Equal(t, 100.0, *got.AdaptiveBaselineMs)
	require.NotNil(t, got.AdaptiveBaselineResumeAt)
}

func TestCentisecondBandStillAlertsAndRecovers(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	windowStart := now.Add(-5 * time.Minute)
	rule := adaptiveLatencyRule(0.02)

	low, high, ok := latencyThresholds(rule)
	require.True(t, ok)
	assert.Less(t, low, 0.02)
	assert.Greater(t, high, 0.02)

	highAlert := evaluateLatencyAnomaly(rule, coveredLatencyStats(1, 1, 1, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, highAlert.Action)

	zero := evaluateLatencyAnomaly(rule, coveredLatencyStats(0, 0, 0, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationNone, zero.Action)
	assert.Equal(t, models.LatencyAlertNormal, zero.Notification.LatencyAlertState)

	rule.LatencyAlertState = models.LatencyAlertHigh
	rule.LatencyIncidentNotified = true
	recovered := evaluateLatencyAnomaly(rule, coveredLatencyStats(0.02, 0.02, 0.02, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationRecovery, recovered.Action)
	assert.Equal(t, models.LatencyAlertNormal, recovered.Notification.LatencyAlertState)

	tiny := adaptiveLatencyRule(0.01)
	one := evaluateLatencyAnomaly(tiny, coveredLatencyStats(1, 1, 1, 18, windowStart, now), metricstore.PingBaselineCandidate{}, windowStart, now, 0)
	assert.Equal(t, pingLatencyNotificationAlertHigh, one.Action)
}

func widenedAdaptiveRule(resume time.Time) models.PingLossNotification {
	rule := adaptiveLatencyRule(100)
	rule.AdaptiveUpperDeviationPercent = 40
	rule.LatencyAlertState = models.LatencyAlertNormal
	rule.LatencyIncidentNotified = false
	rule.AdaptiveBaselineStatus = models.AdaptiveBaselineRecoveryHold
	rule.AdaptiveBaselineResumeAt = &resume
	return rule
}
