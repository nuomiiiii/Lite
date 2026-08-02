package trafficledger

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPointer(value int) *int { return &value }

func TestCalibrationSnapshotUsesFrontendTrafficFieldNames(t *testing.T) {
	payload, err := json.Marshal(CalibrationSnapshot{
		Raw:       Usage{Up: 1, Down: 2},
		Effective: Usage{Up: 3, Down: 4},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"client":"",
		"cycle":"",
		"cycle_start":"0001-01-01T00:00:00Z",
		"cycle_end":"0001-01-01T00:00:00Z",
		"raw":{"up":1,"down":2},
		"adjustment":{"up":0,"down":0},
		"effective":{"up":3,"down":4},
		"history":null
	}`, string(payload))
}

func TestCurrentTrafficCycleClampsResetDayAtMonthEnd(t *testing.T) {
	resetDay := intPointer(31)
	start, cycle, err := CurrentTrafficCycle(resetDay, time.Date(2026, 2, 28, 12, 0, 0, 0, BeijingLocation))
	require.NoError(t, err)
	assert.Equal(t, "2026-02-28", cycle)
	assert.Equal(t, time.Date(2026, 2, 28, 0, 0, 0, 0, BeijingLocation), start)

	start, cycle, err = CurrentTrafficCycle(resetDay, time.Date(2026, 2, 27, 23, 59, 0, 0, BeijingLocation))
	require.NoError(t, err)
	assert.Equal(t, "2026-01-31", cycle)
	assert.Equal(t, time.Date(2026, 1, 31, 0, 0, 0, 0, BeijingLocation), start)
}

func TestTrafficCycleInclusiveEndUsesTheDayBeforeTheNextReset(t *testing.T) {
	assert.Equal(t,
		time.Date(2026, 2, 27, 0, 0, 0, 0, BeijingLocation),
		trafficCycleInclusiveEnd(time.Date(2026, 1, 31, 0, 0, 0, 0, BeijingLocation), 31),
	)
	assert.Equal(t,
		time.Date(2026, 3, 30, 0, 0, 0, 0, BeijingLocation),
		trafficCycleInclusiveEnd(time.Date(2026, 2, 28, 0, 0, 0, 0, BeijingLocation), 31),
	)
}

func TestCalibrationStopsApplyingAfterTheNextResetDay(t *testing.T) {
	resetDay := intPointer(15)
	assert.True(t, calibrationAppliesToCurrentCycle(resetDay, "2026-07-15", time.Date(2026, 8, 14, 23, 59, 0, 0, BeijingLocation)))
	assert.False(t, calibrationAppliesToCurrentCycle(resetDay, "2026-07-15", time.Date(2026, 8, 15, 0, 0, 0, 0, BeijingLocation)))
	assert.True(t, calibrationAppliesToCurrentCycle(resetDay, "2026-08-15", time.Date(2026, 8, 15, 0, 0, 0, 0, BeijingLocation)))
}

func TestAllocateNegativeCalibrationWalksBackwardWithoutNegativeDays(t *testing.T) {
	days := []calibrationDay{
		{Day: "2026-08-01", Effective: Usage{Up: 100}},
		{Day: "2026-08-02", Effective: Usage{Up: 70}},
		{Day: "2026-08-03", Effective: Usage{Up: 50}},
	}
	allocation, err := allocateCalibration(days, -120, func(day calibrationDay) int64 { return day.Effective.Up })
	require.NoError(t, err)
	assert.Equal(t, int64(-50), allocation["2026-08-03"])
	assert.Equal(t, int64(-70), allocation["2026-08-02"])
	assert.Zero(t, allocation["2026-08-01"])

	total := int64(0)
	for _, day := range days {
		value := addSignedNonNegative(day.Effective.Up, allocation[day.Day])
		assert.GreaterOrEqual(t, value, int64(0))
		total += value
	}
	assert.Equal(t, int64(100), total)
}

func TestAdjustedLedgerUsageKeepsDailyAndRangeTotalsConsistent(t *testing.T) {
	db := openLedgerTestDB(t, "calibration-ledger-consistency")
	rows := []models.TrafficDailyLedger{
		{Client: "client-a", Day: "2026-08-01", UpBytes: 100, DownBytes: 200},
		{Client: "client-a", Day: "2026-08-02", UpBytes: 300, DownBytes: 400},
	}
	require.NoError(t, db.Create(&rows).Error)
	adjustments := []models.TrafficCalibrationAdjustment{
		{CalibrationID: "a", Client: "client-a", Cycle: "2026-08-01", Day: "2026-08-01", UpDelta: 50, DownDelta: -25},
		{CalibrationID: "b", Client: "client-a", Cycle: "2026-08-01", Day: "2026-08-02", UpDelta: -75, DownDelta: 100},
	}
	require.NoError(t, db.Create(&adjustments).Error)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, BeijingLocation)
	middle := start.AddDate(0, 0, 1)
	end := start.AddDate(0, 0, 2)

	first, err := AdjustedLedgerUsage(context.Background(), db, "client-a", start, middle)
	require.NoError(t, err)
	second, err := AdjustedLedgerUsage(context.Background(), db, "client-a", middle, end)
	require.NoError(t, err)
	total, err := AdjustedLedgerUsage(context.Background(), db, "client-a", start, end)
	require.NoError(t, err)
	assert.Equal(t, Usage{Up: first.Up + second.Up, Down: first.Down + second.Down}, total)
	assert.Equal(t, Usage{Up: 375, Down: 675}, total)
}

func TestShiftCumulativeCounterMakesNewestPointExact(t *testing.T) {
	assert.Equal(t, int64(180), ShiftCumulativeCounter(130, 150, 200))
	assert.Equal(t, int64(0), ShiftCumulativeCounter(20, 100, 50))
	assert.Equal(t, int64(50), ShiftCumulativeCounter(100, 100, 50))
}
