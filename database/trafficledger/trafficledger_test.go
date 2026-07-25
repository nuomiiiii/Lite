package trafficledger

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openLedgerTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.TrafficReportNotification{},
		&models.TrafficDailyLedger{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a"}).Error)
	return db
}

func TestBeijingDayUsesCalendarDateAcrossUTCBoundary(t *testing.T) {
	got := BeijingDay(time.Date(2026, 7, 31, 16, 30, 0, 0, time.UTC))
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, BeijingLocation)
	assert.True(t, got.Equal(want), "got %s, want %s", got, want)
}

func TestEnsureRangeAndSumAcrossMonthIsIdempotent(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-cross-month")
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, BeijingLocation)
	end := time.Date(2026, 8, 2, 0, 0, 0, 0, BeijingLocation)
	calls := 0
	calculate := func(_ context.Context, _ string, dayStart, _ time.Time) (Usage, error) {
		calls++
		day := int64(dayStart.In(BeijingLocation).Day())
		return Usage{Up: day, Down: day * 10}, nil
	}

	require.NoError(t, ensureRangeWithCalculator(context.Background(), db, []string{"client-a", "client-a"}, start, end, calculate))
	require.Equal(t, 3, calls)
	require.NoError(t, ensureRangeWithCalculator(context.Background(), db, []string{"client-a"}, start, end, calculate))
	assert.Equal(t, 3, calls, "existing days must not be recalculated")

	usage, err := SumRange(context.Background(), db, "client-a", start, end)
	require.NoError(t, err)
	assert.Equal(t, int64(62), usage.Up)
	assert.Equal(t, int64(620), usage.Down)
}

func TestEnsureRangeRetriesOnlyMissingDaysAfterFailure(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-retry")
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, BeijingLocation)
	end := time.Date(2026, 8, 2, 0, 0, 0, 0, BeijingLocation)
	fail := true
	calculate := func(_ context.Context, _ string, dayStart, _ time.Time) (Usage, error) {
		if fail && dayStart.In(BeijingLocation).Day() == 31 {
			return Usage{}, errors.New("temporary metric read failure")
		}
		return Usage{Up: 10, Down: 20}, nil
	}

	require.Error(t, ensureRangeWithCalculator(context.Background(), db, []string{"client-a"}, start, end, calculate))
	var firstCount int64
	require.NoError(t, db.Model(&models.TrafficDailyLedger{}).Count(&firstCount).Error)
	assert.Zero(t, firstCount, "a failed range calculation must not leave partial ledger rows")

	fail = false
	require.NoError(t, ensureRangeWithCalculator(context.Background(), db, []string{"client-a"}, start, end, calculate))
	var finalCount int64
	require.NoError(t, db.Model(&models.TrafficDailyLedger{}).Count(&finalCount).Error)
	assert.Equal(t, int64(3), finalCount)
}

func TestEnsureRangePreservesExistingDaysWhenWindowMoves(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-moving-window")
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, BeijingLocation)
	end := time.Date(2026, 8, 2, 0, 0, 0, 0, BeijingLocation)
	require.NoError(t, db.Create(&models.TrafficDailyLedger{
		Client: "client-a", Day: dayKey(start), UpBytes: 999, DownBytes: 888,
	}).Error)
	calculate := func(_ context.Context, _ string, rangeStart, rangeEnd time.Time) (map[string]Usage, error) {
		result := make(map[string]Usage)
		for day := rangeStart; day.Before(rangeEnd); day = day.AddDate(0, 0, 1) {
			result[dayKey(day)] = Usage{Up: 10, Down: 20}
		}
		return result, nil
	}

	require.NoError(t, ensureRangeWithDailyCalculator(context.Background(), db, []string{"client-a"}, start, end, calculate))
	var original models.TrafficDailyLedger
	require.NoError(t, db.First(&original, "client = ? AND day = ?", "client-a", dayKey(start)).Error)
	assert.Equal(t, int64(999), original.UpBytes)
	assert.Equal(t, int64(888), original.DownBytes)
	complete, err := ledgerRangeComplete(context.Background(), db, "client-a", start, end)
	require.NoError(t, err)
	assert.True(t, complete)
}

func TestDailyAllocationPreservesContinuousTotalAcrossMidnightRecovery(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, BeijingLocation)
	end := start.AddDate(0, 0, 2)
	previous := &DeltaRecord{
		Time:       start.Add(-time.Minute),
		NetTotalUp: 10 * gib,
	}
	records := []DeltaRecord{
		{Time: start.Add(23*time.Hour + 59*time.Minute), NetTotalUp: gib, TrafficUp: gib},
		{Time: start.AddDate(0, 0, 1).Add(time.Minute), NetTotalUp: 10*gib + gib/2, TrafficUp: 9*gib + gib/2},
		{Time: start.AddDate(0, 0, 1).Add(time.Hour), NetTotalUp: 11 * gib, TrafficUp: gib / 2},
	}

	daily := usagesByDayFromRecords(start, end, records, previous)
	directUp, directDown := SumTrafficDeltas(records, previous)
	combined := Usage{}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		usage := daily[dayKey(day)]
		combined.Up += usage.Up
		combined.Down += usage.Down
	}
	assert.Equal(t, directUp, combined.Up)
	assert.Equal(t, directDown, combined.Down)
	assert.Equal(t, int64(0), daily[dayKey(start)].Up)
	assert.Equal(t, gib, daily[dayKey(start.AddDate(0, 0, 1))].Up)
}

func TestSumRangeRejectsPartialLedger(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-partial")
	require.NoError(t, db.Create(&models.TrafficDailyLedger{
		Client: "client-a", Day: "2026-07-30", UpBytes: 10, DownBytes: 20,
	}).Error)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, BeijingLocation)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, BeijingLocation)

	_, err := SumRange(context.Background(), db, "client-a", start, end)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "have 1 of 2 days")
}

func TestMaintainRemovesOnlyExpiredLedgerRows(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-cleanup")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, BeijingLocation)
	cutoff := BeijingDay(now).AddDate(0, 0, -MonthlyLedgerRetentionDays)
	require.NoError(t, db.Create(&models.TrafficReportNotification{
		Client: "client-a", Enable: true, Monthly: true, IncludeTraffic: true,
	}).Error)
	require.NoError(t, db.Create([]models.TrafficDailyLedger{
		{Client: "client-a", Day: cutoff.AddDate(0, 0, -1).Format(time.DateOnly)},
		{Client: "client-a", Day: cutoff.Format(time.DateOnly)},
		{Client: "client-a", Day: BeijingDay(now).AddDate(0, 0, -2).Format(time.DateOnly)},
		{Client: "client-a", Day: BeijingDay(now).AddDate(0, 0, -1).Format(time.DateOnly)},
	}).Error)

	require.NoError(t, Maintain(context.Background(), db, now))
	var rows []models.TrafficDailyLedger
	require.NoError(t, db.Order("day ASC").Find(&rows).Error)
	require.Len(t, rows, 3)
	assert.Equal(t, cutoff.Format(time.DateOnly), rows[0].Day)
}

func TestMaintainDeletesLedgerWhenReportsAreDisabled(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-disabled-cleanup")
	require.NoError(t, db.Create(&models.TrafficDailyLedger{
		Client: "client-a", Day: "2026-07-24", UpBytes: 10,
	}).Error)

	require.NoError(t, Maintain(context.Background(), db, time.Date(2026, 7, 25, 12, 0, 0, 0, BeijingLocation)))
	var count int64
	require.NoError(t, db.Model(&models.TrafficDailyLedger{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestReportLedgerRetentionFollowsLongestEnabledCadence(t *testing.T) {
	assert.Equal(t, 0, reportLedgerRetentionDays(models.TrafficReportNotification{}))
	assert.Equal(t, DailyLedgerRetentionDays, reportLedgerRetentionDays(models.TrafficReportNotification{Daily: true}))
	assert.Equal(t, WeeklyLedgerRetentionDays, reportLedgerRetentionDays(models.TrafficReportNotification{Daily: true, Weekly: true}))
	assert.Equal(t, MonthlyLedgerRetentionDays, reportLedgerRetentionDays(models.TrafficReportNotification{Daily: true, Weekly: true, Monthly: true}))
}
