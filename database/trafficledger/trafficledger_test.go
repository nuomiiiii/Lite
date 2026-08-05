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
		&models.TrafficCalibrationAdjustment{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a"}).Error)
	return db
}

func TestBeijingDayUsesCalendarDateAcrossUTCBoundary(t *testing.T) {
	got := BeijingDay(time.Date(2026, 7, 31, 16, 30, 0, 0, time.UTC))
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, BeijingLocation)
	assert.True(t, got.Equal(want), "got %s, want %s", got, want)
}

func TestUsageByHourFromRecordsUsesBeijingBuckets(t *testing.T) {
	start := time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC)
	total, hourly, err := usageByHourFromRecords([]DeltaRecord{
		{Time: start.Add(10 * time.Minute), NetTotalUp: 130, NetTotalDown: 240, TrafficUp: 30, TrafficDown: 40},
		{Time: start.Add(70 * time.Minute), NetTotalUp: 180, NetTotalDown: 300, TrafficUp: 50, TrafficDown: 60},
	}, &DeltaRecord{Time: start.Add(-time.Minute), NetTotalUp: 100, NetTotalDown: 200})
	require.NoError(t, err)
	assert.Equal(t, Usage{Up: 80, Down: 100}, total)
	require.Len(t, hourly, 2)
	assert.Equal(t, 0, hourly[0].Hour.In(BeijingLocation).Hour())
	assert.Equal(t, Usage{Up: 30, Down: 40}, hourly[0].Usage)
	assert.Equal(t, 1, hourly[1].Hour.In(BeijingLocation).Hour())
	assert.Equal(t, Usage{Up: 50, Down: 60}, hourly[1].Usage)
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

func TestSumTrafficDeltasStartsNewEpochAfterPersistentCounterDrop(t *testing.T) {
	base := time.Date(2026, 8, 5, 1, 27, 0, 0, BeijingLocation)
	previous := &DeltaRecord{Time: base.Add(-time.Minute), NetTotalUp: 100, NetTotalDown: 200}
	records := []DeltaRecord{
		{
			Time: base, NetTotalUp: 80, NetTotalDown: 150,
			TrafficUp: 80, TrafficDown: 150, TrafficUpSet: true, TrafficDownSet: true,
		},
		{
			Time: base.Add(time.Minute), NetTotalUp: 90, NetTotalDown: 160,
			TrafficUp: 10, TrafficDown: 10, TrafficUpSet: true, TrafficDownSet: true,
		},
	}

	up, down := SumTrafficDeltas(records, previous)
	assert.Equal(t, int64(10), up)
	assert.Equal(t, int64(10), down)
}

func TestSumTrafficDeltasCorrelatesAsymmetricRecoveryAfterScopeChange(t *testing.T) {
	base := time.Date(2026, 8, 5, 1, 27, 0, 0, BeijingLocation)
	previous := &DeltaRecord{Time: base.Add(-time.Minute), NetTotalUp: 100, NetTotalDown: 200}
	records := []DeltaRecord{
		{
			Time: base, NetTotalUp: 80, NetTotalDown: 150,
			TrafficUp: 80, TrafficDown: 150, TrafficUpSet: true, TrafficDownSet: true,
		},
		{
			Time: base.Add(time.Minute), NetTotalUp: 110, NetTotalDown: 160,
			TrafficUp: 30, TrafficDown: 10, TrafficUpSet: true, TrafficDownSet: true,
		},
	}

	up, down := SumTrafficDeltas(records, previous)
	assert.Equal(t, int64(30), up)
	assert.Equal(t, int64(10), down)
}

func TestSumTrafficDeltasIgnoresTemporaryCorrelatedBadReading(t *testing.T) {
	base := time.Date(2026, 8, 5, 1, 27, 0, 0, BeijingLocation)
	previous := &DeltaRecord{Time: base.Add(-time.Minute), NetTotalUp: 100, NetTotalDown: 200}
	records := []DeltaRecord{
		{
			Time: base, NetTotalUp: 80, NetTotalDown: 150,
			TrafficUp: 80, TrafficDown: 150, TrafficUpSet: true, TrafficDownSet: true,
		},
		{
			Time: base.Add(time.Minute), NetTotalUp: 110, NetTotalDown: 220,
			TrafficUp: 30, TrafficDown: 70, TrafficUpSet: true, TrafficDownSet: true,
		},
	}

	up, down := SumTrafficDeltas(records, previous)
	assert.Equal(t, int64(10), up)
	assert.Equal(t, int64(20), down)
}

func TestSumTrafficDeltasCountsOnlyAfterCounterResetBaseline(t *testing.T) {
	base := time.Date(2026, 8, 5, 1, 27, 0, 0, BeijingLocation)
	previous := &DeltaRecord{Time: base.Add(-time.Minute), NetTotalUp: 100, NetTotalDown: 200}
	records := []DeltaRecord{
		{
			Time: base, NetTotalUp: 10, NetTotalDown: 20,
			TrafficUpSet: true, TrafficDownSet: true,
		},
		{
			Time: base.Add(time.Minute), NetTotalUp: 15, NetTotalDown: 30,
			TrafficUp: 5, TrafficDown: 10, TrafficUpSet: true, TrafficDownSet: true,
		},
	}

	up, down := SumTrafficDeltas(records, previous)
	assert.Equal(t, int64(5), up)
	assert.Equal(t, int64(10), down)
}

func TestTrafficDeltaOrFallbackDistinguishesStoredZeroFromLegacyMissingMetric(t *testing.T) {
	assert.Equal(t, int64(0), TrafficDeltaOrFallback(0, true, 150, 100))
	assert.Equal(t, int64(50), TrafficDeltaOrFallback(0, false, 150, 100))
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

	require.NoError(t, maintainWithDailyCalculator(context.Background(), db, now, zeroDailyUsage))
	var rows []models.TrafficDailyLedger
	require.NoError(t, db.Order("day ASC").Find(&rows).Error)
	require.Len(t, rows, DashboardHistoryDays)
	assert.Equal(t, cutoff.Format(time.DateOnly), rows[0].Day)
}

func TestMaintainKeepsDashboardLedgerWhenReportsAreDisabled(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-disabled-cleanup")
	require.NoError(t, db.Create(&models.TrafficDailyLedger{
		Client: "client-a", Day: "2026-07-24", UpBytes: 10,
	}).Error)

	require.NoError(t, maintainWithDailyCalculator(context.Background(), db, time.Date(2026, 7, 25, 12, 0, 0, 0, BeijingLocation), zeroDailyUsage))
	var count int64
	require.NoError(t, db.Model(&models.TrafficDailyLedger{}).Count(&count).Error)
	assert.Equal(t, int64(DashboardHistoryDays-1), count)
}

func TestMaintainUsesLongestDashboardOrReportRetention(t *testing.T) {
	db := openLedgerTestDB(t, "traffic-ledger-dashboard-retention")
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, BeijingLocation)
	require.NoError(t, db.Create(&models.TrafficReportNotification{
		Client: "client-a", Enable: true, Monthly: true,
	}).Error)
	require.NoError(t, db.Create([]models.TrafficDailyLedger{
		{Client: "client-a", Day: BeijingDay(now).AddDate(0, 0, -MonthlyLedgerRetentionDays-1).Format(time.DateOnly)},
		{Client: "client-a", Day: BeijingDay(now).AddDate(0, 0, -MonthlyLedgerRetentionDays).Format(time.DateOnly)},
	}).Error)

	require.NoError(t, maintainWithDailyCalculator(context.Background(), db, now, zeroDailyUsage))
	var rows []models.TrafficDailyLedger
	require.NoError(t, db.Where("client = ?", "client-a").Order("day ASC").Find(&rows).Error)
	assert.Equal(t, BeijingDay(now).AddDate(0, 0, -MonthlyLedgerRetentionDays).Format(time.DateOnly), rows[0].Day)
}

func TestBillableUsage(t *testing.T) {
	assert.Equal(t, int64(10), BillableUsage("up", 10, 20))
	assert.Equal(t, int64(20), BillableUsage("down", 10, 20))
	assert.Equal(t, int64(30), BillableUsage("sum", 10, 20))
	assert.Equal(t, int64(10), BillableUsage("min", 10, 20))
	assert.Equal(t, int64(20), BillableUsage("max", 10, 20))
	assert.Equal(t, int64(30), BillableUsage(" SUM ", 10, 20))
	assert.Equal(t, int64(20), BillableUsage("", 10, 20))
}

func zeroDailyUsage(_ context.Context, _ string, start, end time.Time) (map[string]Usage, error) {
	result := make(map[string]Usage)
	for day := BeijingDay(start); day.Before(BeijingDay(end)); day = day.AddDate(0, 0, 1) {
		result[dayKey(day)] = Usage{}
	}
	return result, nil
}

func TestReportLedgerRetentionFollowsLongestEnabledCadence(t *testing.T) {
	assert.Equal(t, 0, reportLedgerRetentionDays(models.TrafficReportNotification{}))
	assert.Equal(t, DailyLedgerRetentionDays, reportLedgerRetentionDays(models.TrafficReportNotification{Daily: true}))
	assert.Equal(t, WeeklyLedgerRetentionDays, reportLedgerRetentionDays(models.TrafficReportNotification{Daily: true, Weekly: true}))
	assert.Equal(t, MonthlyLedgerRetentionDays, reportLedgerRetentionDays(models.TrafficReportNotification{Daily: true, Weekly: true, Monthly: true}))
}
