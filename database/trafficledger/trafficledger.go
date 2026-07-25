package trafficledger

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	DailyLedgerRetentionDays   = 2
	WeeklyLedgerRetentionDays  = 8
	MonthlyLedgerRetentionDays = 35
	// MetricSafetyRetentionDays leaves enough metric history to settle a missed
	// day without retaining an entire report month in the metric store.
	MetricSafetyRetentionDays = 2
)

var (
	BeijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)
	ensureMu        sync.Mutex
)

type Usage struct {
	Up   int64
	Down int64
}

type DeltaRecord struct {
	Time         time.Time
	NetTotalUp   int64
	NetTotalDown int64
	TrafficUp    int64
	TrafficDown  int64
}

type usageCalculator func(context.Context, string, time.Time, time.Time) (Usage, error)
type dailyUsageCalculator func(context.Context, string, time.Time, time.Time) (map[string]Usage, error)

// BeijingDay returns the start of t's Beijing calendar day.
func BeijingDay(t time.Time) time.Time {
	local := t.In(BeijingLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, BeijingLocation)
}

func dayKey(day time.Time) string {
	return BeijingDay(day).Format(time.DateOnly)
}

func dayCount(startDay, endDay time.Time) int {
	count := 0
	for day := BeijingDay(startDay); day.Before(BeijingDay(endDay)); day = day.AddDate(0, 0, 1) {
		count++
	}
	return count
}

func normalizeRange(startDay, endDay time.Time) (time.Time, time.Time, error) {
	start := BeijingDay(startDay)
	end := BeijingDay(endDay)
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("traffic ledger range must contain at least one complete day")
	}
	return start, end, nil
}

type reportTarget struct {
	clientID      string
	retentionDays int
}

func reportLedgerRetentionDays(notification models.TrafficReportNotification) int {
	switch {
	case notification.Monthly:
		return MonthlyLedgerRetentionDays
	case notification.Weekly:
		return WeeklyLedgerRetentionDays
	case notification.Daily:
		return DailyLedgerRetentionDays
	default:
		return 0
	}
}

func enabledReportTargets(ctx context.Context, db *gorm.DB) ([]reportTarget, error) {
	var notifications []models.TrafficReportNotification
	if err := db.WithContext(ctx).
		Select("client", "daily", "weekly", "monthly").
		Where("enable = ?", true).
		Find(&notifications).Error; err != nil {
		return nil, fmt.Errorf("list enabled traffic report clients: %w", err)
	}
	targetsByClient := make(map[string]int, len(notifications))
	for _, notification := range notifications {
		retentionDays := reportLedgerRetentionDays(notification)
		if notification.Client == "" || retentionDays == 0 {
			continue
		}
		if retentionDays > targetsByClient[notification.Client] {
			targetsByClient[notification.Client] = retentionDays
		}
	}
	clientIDs := make([]string, 0, len(targetsByClient))
	for clientID := range targetsByClient {
		clientIDs = append(clientIDs, clientID)
	}
	sort.Strings(clientIDs)
	targets := make([]reportTarget, 0, len(clientIDs))
	for _, clientID := range clientIDs {
		targets = append(targets, reportTarget{clientID: clientID, retentionDays: targetsByClient[clientID]})
	}
	return targets, nil
}

// BackfillEnabledHistory converts the history retained by the old report
// implementation before callers lower any metric retention.
func BackfillEnabledHistory(ctx context.Context, db *gorm.DB, now time.Time) error {
	targets, err := enabledReportTargets(ctx, db)
	if err != nil {
		return err
	}
	today := BeijingDay(now)
	for _, target := range targets {
		start := today.AddDate(0, 0, -target.retentionDays)
		hasRows, err := clientHasLedgerRows(ctx, db, target.clientID)
		if err != nil {
			return err
		}
		if hasRows {
			recentStart := today.AddDate(0, 0, -MetricSafetyRetentionDays)
			if recentStart.Before(start) {
				recentStart = start
			}
			if err := EnsureRange(ctx, db, []string{target.clientID}, recentStart, today); err != nil {
				return err
			}
			complete, err := ledgerRangeComplete(ctx, db, target.clientID, start, today)
			if err != nil {
				return err
			}
			if complete {
				continue
			}
		}
		if err := EnsureRange(ctx, db, []string{target.clientID}, start, today); err != nil {
			return err
		}
		// Existing rows may come from a previously shorter report cadence and
		// cannot be revalidated after their source retention expires. A brand-new
		// ledger, however, must match the old continuous report calculation before
		// any source retention can be shortened.
		if hasRows {
			continue
		}
		ledgerUsage, err := SumRange(ctx, db, target.clientID, start, today)
		if err != nil {
			return err
		}
		metricUsage, err := MetricUsage(ctx, target.clientID, start.UTC(), today.UTC().Add(-time.Nanosecond))
		if err != nil {
			return fmt.Errorf("validate traffic ledger for client %s: %w", target.clientID, err)
		}
		if ledgerUsage != metricUsage {
			return fmt.Errorf(
				"traffic ledger validation failed for client %s: ledger up/down=%d/%d, metrics up/down=%d/%d",
				target.clientID,
				ledgerUsage.Up,
				ledgerUsage.Down,
				metricUsage.Up,
				metricUsage.Down,
			)
		}
	}
	return nil
}

func clientHasLedgerRows(ctx context.Context, db *gorm.DB, clientID string) (bool, error) {
	var count int64
	if err := db.WithContext(ctx).Model(&models.TrafficDailyLedger{}).
		Where("client = ?", clientID).Limit(1).Count(&count).Error; err != nil {
		return false, fmt.Errorf("inspect traffic ledger for client %s: %w", clientID, err)
	}
	return count > 0, nil
}

func ledgerRangeComplete(ctx context.Context, db *gorm.DB, clientID string, startDay, endDay time.Time) (bool, error) {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return false, err
	}
	var count int64
	if err := db.WithContext(ctx).Model(&models.TrafficDailyLedger{}).
		Where("client = ? AND day >= ? AND day < ?", clientID, dayKey(start), dayKey(end)).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("inspect traffic ledger range for client %s: %w", clientID, err)
	}
	return count == int64(dayCount(start, end)), nil
}

// Maintain settles recent missing days and removes old ledger rows. Existing
// rows are never recalculated, so running this hourly has negligible cost.
func Maintain(ctx context.Context, db *gorm.DB, now time.Time) error {
	targets, err := enabledReportTargets(ctx, db)
	if err != nil {
		return err
	}
	today := BeijingDay(now)
	clientIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		clientIDs = append(clientIDs, target.clientID)
	}
	if len(clientIDs) > 0 {
		if err := EnsureRange(ctx, db, clientIDs, today.AddDate(0, 0, -MetricSafetyRetentionDays), today); err != nil {
			return err
		}
	}
	for _, target := range targets {
		cutoff := dayKey(today.AddDate(0, 0, -target.retentionDays))
		if err := db.WithContext(ctx).
			Where("client = ? AND day < ?", target.clientID, cutoff).
			Delete(&models.TrafficDailyLedger{}).Error; err != nil {
			return fmt.Errorf("clean expired traffic ledger rows for client %s: %w", target.clientID, err)
		}
	}
	if len(clientIDs) == 0 {
		if err := db.WithContext(ctx).Where("client <> ?", "").Delete(&models.TrafficDailyLedger{}).Error; err != nil {
			return fmt.Errorf("clean disabled traffic ledger rows: %w", err)
		}
	} else if err := db.WithContext(ctx).Where("client NOT IN ?", clientIDs).Delete(&models.TrafficDailyLedger{}).Error; err != nil {
		return fmt.Errorf("clean disabled traffic ledger rows: %w", err)
	}
	return nil
}

// EnsureRange creates any missing per-day rows in [startDay, endDay). The
// process is idempotent and serialised with report-time settlement.
func EnsureRange(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time) error {
	return ensureRangeWithDailyCalculator(ctx, db, clientIDs, startDay, endDay, MetricUsagesByDay)
}

func ensureRangeWithCalculator(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time, calculate usageCalculator) error {
	if calculate == nil {
		return fmt.Errorf("traffic ledger calculator is nil")
	}
	return ensureRangeWithDailyCalculator(ctx, db, clientIDs, startDay, endDay,
		func(ctx context.Context, clientID string, start, end time.Time) (map[string]Usage, error) {
			result := make(map[string]Usage)
			for day := BeijingDay(start); day.Before(BeijingDay(end)); day = day.AddDate(0, 0, 1) {
				usage, err := calculate(ctx, clientID, day.UTC(), day.AddDate(0, 0, 1).UTC().Add(-time.Nanosecond))
				if err != nil {
					return nil, err
				}
				result[dayKey(day)] = usage
			}
			return result, nil
		})
}

func ensureRangeWithDailyCalculator(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time, calculate dailyUsageCalculator) error {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return err
	}
	if calculate == nil {
		return fmt.Errorf("traffic ledger calculator is nil")
	}

	ids := make([]string, 0, len(clientIDs))
	seenIDs := make(map[string]struct{}, len(clientIDs))
	for _, clientID := range clientIDs {
		if clientID == "" {
			continue
		}
		if _, ok := seenIDs[clientID]; ok {
			continue
		}
		seenIDs[clientID] = struct{}{}
		ids = append(ids, clientID)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)

	ensureMu.Lock()
	defer ensureMu.Unlock()

	var existingRows []models.TrafficDailyLedger
	if err := db.WithContext(ctx).
		Select("client", "day").
		Where("client IN ? AND day >= ? AND day < ?", ids, dayKey(start), dayKey(end)).
		Find(&existingRows).Error; err != nil {
		return fmt.Errorf("list existing traffic ledger rows: %w", err)
	}
	existing := make(map[string]struct{}, len(existingRows))
	for _, row := range existingRows {
		existing[row.Client+"\x00"+row.Day] = struct{}{}
	}

	for _, clientID := range ids {
		complete := true
		for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
			if _, ok := existing[clientID+"\x00"+dayKey(day)]; !ok {
				complete = false
				break
			}
		}
		if complete {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		dailyUsage, err := calculate(ctx, clientID, start, end)
		if err != nil {
			return fmt.Errorf("settle traffic ledger for client %s: %w", clientID, err)
		}
		rows := make([]models.TrafficDailyLedger, 0, dayCount(start, end))
		for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
			key := dayKey(day)
			usage, ok := dailyUsage[key]
			if !ok {
				return fmt.Errorf("settle traffic ledger for client %s: calculator omitted %s", clientID, key)
			}
			if _, ok := existing[clientID+"\x00"+key]; ok {
				continue
			}
			rows = append(rows, models.TrafficDailyLedger{
				Client: clientID, Day: key, UpBytes: usage.Up, DownBytes: usage.Down,
			})
		}
		if len(rows) == 0 {
			continue
		}
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "client"}, {Name: "day"}},
				DoUpdates: clause.AssignmentColumns([]string{"up_bytes", "down_bytes", "updated_at"}),
			}).Create(&rows).Error
		}); err != nil {
			return fmt.Errorf("write traffic ledger for client %s: %w", clientID, err)
		}
		for _, row := range rows {
			existing[clientID+"\x00"+row.Day] = struct{}{}
		}
	}
	return nil
}

// SumRange reads a complete ledger interval. Missing rows are treated as an
// error rather than silently producing a partial weekly or monthly report.
func SumRange(ctx context.Context, db *gorm.DB, clientID string, startDay, endDay time.Time) (Usage, error) {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return Usage{}, err
	}
	var rows []models.TrafficDailyLedger
	if err := db.WithContext(ctx).
		Select("day", "up_bytes", "down_bytes").
		Where("client = ? AND day >= ? AND day < ?", clientID, dayKey(start), dayKey(end)).
		Order("day ASC").
		Find(&rows).Error; err != nil {
		return Usage{}, fmt.Errorf("read traffic ledger for client %s: %w", clientID, err)
	}
	expected := dayCount(start, end)
	if len(rows) != expected {
		return Usage{}, fmt.Errorf("traffic ledger for client %s is incomplete: have %d of %d days", clientID, len(rows), expected)
	}
	usage := Usage{}
	for _, row := range rows {
		usage.Up += row.UpBytes
		usage.Down += row.DownBytes
	}
	return usage, nil
}

// MetricUsage computes exact report traffic from retained metric points. The
// end boundary is inclusive to match metric-store range queries.
func MetricUsage(ctx context.Context, clientID string, start, end time.Time) (Usage, error) {
	if end.Before(start) {
		return Usage{}, fmt.Errorf("traffic metric range end precedes start")
	}
	records, previous, err := metricRecordsAndBaseline(ctx, clientID, start, end)
	if err != nil {
		return Usage{}, err
	}
	up, down := SumTrafficDeltas(records, previous)
	return Usage{Up: up, Down: down}, nil
}

// MetricUsagesByDay scans the full interval once, applies counter recovery as
// one continuous state machine, then assigns each accepted delta to a Beijing
// calendar day. This makes daily rows additive across weekly/monthly reports.
func MetricUsagesByDay(ctx context.Context, clientID string, startDay, endDay time.Time) (map[string]Usage, error) {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return nil, err
	}
	records, previous, err := metricRecordsAndBaseline(ctx, clientID, start.UTC(), end.UTC().Add(-time.Nanosecond))
	if err != nil {
		return nil, err
	}
	return usagesByDayFromRecords(start, end, records, previous), nil
}

func usagesByDayFromRecords(start, end time.Time, records []DeltaRecord, previous *DeltaRecord) map[string]Usage {
	result := make(map[string]Usage, dayCount(start, end))
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		result[dayKey(day)] = Usage{}
	}

	hasPrevious := previous != nil
	var previousUp, previousDown int64
	if previous != nil {
		previousUp = previous.NetTotalUp
		previousDown = previous.NetTotalDown
	}
	upDeltas := trafficDeltasByRecord(records, hasPrevious, previousUp,
		func(record DeltaRecord) int64 { return record.NetTotalUp },
		func(record DeltaRecord) int64 { return record.TrafficUp })
	downDeltas := trafficDeltasByRecord(records, hasPrevious, previousDown,
		func(record DeltaRecord) int64 { return record.NetTotalDown },
		func(record DeltaRecord) int64 { return record.TrafficDown })
	for i, record := range records {
		key := dayKey(record.Time)
		usage, ok := result[key]
		if !ok {
			continue
		}
		usage.Up += upDeltas[i]
		usage.Down += downDeltas[i]
		result[key] = usage
	}
	return result
}

func metricRecordsAndBaseline(ctx context.Context, clientID string, start, end time.Time) ([]DeltaRecord, *DeltaRecord, error) {
	recordsFromStore, err := metricstore.GetTrafficRecordsByClientAndTime(ctx, clientID, start, end)
	if err != nil {
		return nil, nil, err
	}
	records := make([]DeltaRecord, 0, len(recordsFromStore))
	for _, record := range recordsFromStore {
		records = append(records, DeltaRecord{
			Time:         record.Time,
			NetTotalUp:   record.NetTotalUp,
			NetTotalDown: record.NetTotalDown,
			TrafficUp:    record.TrafficUp,
			TrafficDown:  record.TrafficDown,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Time.Before(records[j].Time)
	})

	var previous *DeltaRecord
	baseline, err := metricstore.GetLatestTrafficBefore(ctx, []string{clientID}, start)
	if err != nil {
		return nil, nil, err
	}
	if base, ok := baseline[clientID]; ok {
		previous = &DeltaRecord{
			Time:         base.Time,
			NetTotalUp:   base.NetTotalUp,
			NetTotalDown: base.NetTotalDown,
		}
	}
	return records, previous, nil
}

const (
	trafficCounterRecoveryWindow  = 30 * time.Minute
	trafficDeltaAnomalyMultiplier = int64(4)
	trafficDeltaAnomalyAllowance  = int64(64 * 1024 * 1024)
)

func SumTrafficDeltas(records []DeltaRecord, previous *DeltaRecord) (int64, int64) {
	hasPrevious := previous != nil
	var previousUp int64
	var previousDown int64
	if previous != nil {
		previousUp = previous.NetTotalUp
		previousDown = previous.NetTotalDown
	}
	up := sumInt64s(trafficDeltasByRecord(records, hasPrevious, previousUp,
		func(record DeltaRecord) int64 { return record.NetTotalUp },
		func(record DeltaRecord) int64 { return record.TrafficUp }))
	down := sumInt64s(trafficDeltasByRecord(records, hasPrevious, previousDown,
		func(record DeltaRecord) int64 { return record.NetTotalDown },
		func(record DeltaRecord) int64 { return record.TrafficDown }))
	return up, down
}

func trafficDeltasByRecord(records []DeltaRecord, hasBaseline bool, baseline int64, totalValue, storedDelta func(DeltaRecord) int64) []int64 {
	deltas := make([]int64, len(records))
	for i := 0; i < len(records); i++ {
		current := totalValue(records[i])
		if hasBaseline && current < baseline {
			if recoveryIndex := findCounterRecovery(records, i+1, baseline, records[i].Time, totalValue); recoveryIndex >= 0 {
				recovered := totalValue(records[recoveryIndex])
				deltas[recoveryIndex] += recovered - baseline
				baseline = recovered
				i = recoveryIndex
				continue
			}
		}

		delta := storedDelta(records[i])
		if hasBaseline {
			delta = TrafficDeltaOrFallback(delta, current, baseline)
			if current >= baseline {
				directDelta := current - baseline
				if delta > trafficDeltaUpperBound(directDelta) {
					delta = directDelta
				}
			}
		}
		deltas[i] += delta
		baseline = current
		hasBaseline = true
	}
	return deltas
}

func sumInt64s(values []int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}

func findCounterRecovery(records []DeltaRecord, start int, baseline int64, dropTime time.Time, totalValue func(DeltaRecord) int64) int {
	for i := start; i < len(records); i++ {
		if records[i].Time.Sub(dropTime) > trafficCounterRecoveryWindow {
			break
		}
		if totalValue(records[i]) >= baseline {
			return i
		}
	}
	return -1
}

func trafficDeltaUpperBound(directDelta int64) int64 {
	if directDelta > (math.MaxInt64-trafficDeltaAnomalyAllowance)/trafficDeltaAnomalyMultiplier {
		return math.MaxInt64
	}
	return directDelta*trafficDeltaAnomalyMultiplier + trafficDeltaAnomalyAllowance
}

func TrafficDeltaOrFallback(storedDelta, currentTotal, previousTotal int64) int64 {
	if storedDelta > 0 {
		return storedDelta
	}
	return metricstore.TrafficCounterDelta(currentTotal, previousTotal)
}
