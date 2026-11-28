package trafficledger

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setTestCalibrationMetricUsage(t *testing.T, fn func(context.Context, string, time.Time, time.Time) (Usage, error)) {
	t.Helper()
	prev := metricUsageForCalibration
	metricUsageForCalibration = fn
	t.Cleanup(func() { metricUsageForCalibration = prev })
}

func TestCalibrateToZeroWritesAnchorWhenEffectiveAlreadyZero(t *testing.T) {
	db := openCalibrationTestDB(t, "calibration-zero-anchor")
	InvalidateCalibratedCycleCache()
	t.Cleanup(InvalidateCalibratedCycleCache)

	day := 15
	client := models.Client{
		UUID:            "node-zero",
		TrafficResetDay: &day,
	}
	require.NoError(t, db.Create(&client).Error)

	setTestCalibrationMetricUsage(t, func(context.Context, string, time.Time, time.Time) (Usage, error) {
		return Usage{}, nil
	})

	now := time.Date(2026, time.November, 15, 10, 0, 0, 0, time.UTC)
	snap, err := CalibrateCurrentCycle(context.Background(), db, client, Usage{Up: 0, Down: 0}, "admin", now)
	require.NoError(t, err)
	assert.Equal(t, Usage{Up: 0, Down: 0}, snap.Effective)
	require.Len(t, snap.History, 1)

	usages, unavailable, err := CurrentCalibratedCycleState(context.Background(), db, now)
	require.NoError(t, err)
	assert.Empty(t, unavailable)
	val, ok := usages[client.UUID]
	require.True(t, ok, "zero-delta calibration must anchor the client in calibrated usages")
	assert.Equal(t, Usage{Up: 0, Down: 0}, val)
}

func TestCurrentCalibratedCycleStateMarksUnavailableOnMetricError(t *testing.T) {
	db := openCalibrationTestDB(t, "calibration-unavailable-on-error")
	InvalidateCalibratedCycleCache()
	t.Cleanup(InvalidateCalibratedCycleCache)

	day := 15
	client := models.Client{
		UUID:            "node-err",
		TrafficResetDay: &day,
	}
	require.NoError(t, db.Create(&client).Error)

	now := time.Date(2026, time.November, 15, 10, 0, 0, 0, time.UTC)
	setTestCalibrationMetricUsage(t, func(context.Context, string, time.Time, time.Time) (Usage, error) {
		return Usage{Up: 500, Down: 500}, nil
	})
	_, err := CalibrateCurrentCycle(context.Background(), db, client, Usage{Up: 0, Down: 0}, "admin", now)
	require.NoError(t, err)

	setTestCalibrationMetricUsage(t, func(context.Context, string, time.Time, time.Time) (Usage, error) {
		return Usage{}, errors.New("metric store unavailable")
	})
	InvalidateCalibratedCycleCache()

	usages, unavailable, err := CurrentCalibratedCycleState(context.Background(), db, now)
	require.NoError(t, err)
	_, inUsages := usages[client.UUID]
	assert.False(t, inUsages)
	_, inUnavailable := unavailable[client.UUID]
	assert.True(t, inUnavailable, "calibrated client with metric read error must be marked unavailable")
}

func TestRemapCalibrationOnScheduleChangePreservesActiveCalibration(t *testing.T) {
	db := openCalibrationTestDB(t, "calibration-remap-schedule")
	InvalidateCalibratedCycleCache()
	t.Cleanup(InvalidateCalibratedCycleCache)

	oldDay := 15
	client := models.Client{
		UUID:            "node-remap",
		TrafficResetDay: &oldDay,
	}
	require.NoError(t, db.Create(&client).Error)

	now := time.Date(2026, time.November, 15, 10, 0, 0, 0, time.UTC)
	setTestCalibrationMetricUsage(t, func(context.Context, string, time.Time, time.Time) (Usage, error) {
		return Usage{Up: 300, Down: 200}, nil
	})
	_, err := CalibrateCurrentCycle(context.Background(), db, client, Usage{Up: 0, Down: 0}, "admin", now)
	require.NoError(t, err)

	newDay := 15
	updated := client
	updated.TrafficResetDay = &newDay
	updated.TrafficResetTime = "04:00:00"
	updated.TrafficResetTimezone = "UTC"
	require.NoError(t, db.Model(&models.Client{}).Where("uuid = ?", client.UUID).Updates(map[string]any{
		"traffic_reset_day":      newDay,
		"traffic_reset_time":     updated.TrafficResetTime,
		"traffic_reset_timezone": updated.TrafficResetTimezone,
	}).Error)

	require.NoError(t, RemapCalibrationOnScheduleChange(context.Background(), db, client, updated, now))

	usages, unavailable, err := CurrentCalibratedCycleState(context.Background(), db, now)
	require.NoError(t, err)
	assert.Empty(t, unavailable)
	val, ok := usages[client.UUID]
	require.True(t, ok, "remapped calibration must remain active under the new cycle")
	assert.Equal(t, Usage{Up: 0, Down: 0}, val)
}

func TestRemapCalibrationDoesNotDiscardRetainedFirstDay(t *testing.T) {
	db := openCalibrationTestDB(t, "calibration-remap-retained-first-day")
	InvalidateCalibratedCycleCache()
	t.Cleanup(InvalidateCalibratedCycleCache)

	oldDay := 15
	client := models.Client{
		UUID:            "node-retained",
		TrafficResetDay: &oldDay,
	}
	require.NoError(t, db.Create(&client).Error)

	// now is Nov 15 02:00 UTC (Nov 15 10:00 CST).
	// New reset is Nov 14 12:00 UTC (Nov 14 20:00 CST), which is yesterday in CST
	// but only 14 hours ago (< 24h retention window).
	now := time.Date(2026, time.November, 15, 2, 0, 0, 0, time.UTC)
	setTestCalibrationMetricUsage(t, func(context.Context, string, time.Time, time.Time) (Usage, error) {
		return Usage{Up: 100, Down: 100}, nil
	})
	_, err := CalibrateCurrentCycle(context.Background(), db, client, Usage{Up: 0, Down: 0}, "admin", now)
	require.NoError(t, err)

	newDay := 14
	updated := client
	updated.TrafficResetDay = &newDay
	updated.TrafficResetTime = "12:00:00"
	updated.TrafficResetTimezone = "UTC"
	require.NoError(t, RemapCalibrationOnScheduleChange(context.Background(), db, client, updated, now))

	var firstDayCount int64
	require.NoError(t, db.Model(&models.TrafficCycleFirstDay{}).Where("client = ?", client.UUID).Count(&firstDayCount).Error)
	assert.Equal(t, int64(0), firstDayCount, "first day still within 24h retention must not be pre-sealed as recovered zero")
}
