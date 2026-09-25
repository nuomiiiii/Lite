package trafficledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/trafficreset"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const calibratedCycleCacheTTL = 15 * time.Second

type SignedUsage struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type CalibrationHistory struct {
	CalibrationID string      `json:"calibration_id"`
	Target        Usage       `json:"target"`
	Adjustment    SignedUsage `json:"adjustment"`
	Operator      string      `json:"operator,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
}

type CalibrationSnapshot struct {
	Client          string               `json:"client"`
	Cycle           string               `json:"cycle"`
	CycleStart      time.Time            `json:"cycle_start"`
	CycleEnd        time.Time            `json:"cycle_end"`
	Raw             Usage                `json:"raw"`
	Adjustment      SignedUsage          `json:"adjustment"`
	Effective       Usage                `json:"effective"`
	History         []CalibrationHistory `json:"history"`
	HistoryComplete bool                 `json:"history_complete"`
}

type calibrationDay struct {
	Day        string
	Raw        Usage
	Adjustment SignedUsage
	Effective  Usage
}

type calibratedCycleCacheState struct {
	sync.Mutex
	expires time.Time
	values  map[string]Usage
}

var calibratedCycleCache calibratedCycleCacheState

var metricUsageForCalibration = MetricUsage

func cycleClosedEnd(schedule trafficreset.Schedule, cycleStart time.Time) time.Time {
	next := schedule.Next(cycleStart)
	if next.IsZero() {
		return time.Time{}
	}
	return next.Add(-time.Nanosecond)
}

func isBeijingMidnight(t time.Time) bool {
	return !t.IsZero() && t.In(BeijingLocation).Equal(BeijingDay(t))
}

func clientSchedule(client models.Client) trafficreset.Schedule {
	return trafficreset.FromFields(client.TrafficResetDay, client.TrafficResetTime, client.TrafficResetTimezone)
}

func scheduleForDay(resetDay int) trafficreset.Schedule {
	day := resetDay
	return trafficreset.FromFields(&day, "", "")
}

func NormalizedResetDay(resetDay *int) int {
	if resetDay == nil || *resetDay < 1 || *resetDay > 31 {
		return 1
	}
	return *resetDay
}

func CurrentTrafficCycleFor(client models.Client, now time.Time) (time.Time, string, error) {
	schedule := clientSchedule(client)
	if !schedule.Active() {
		return time.Time{}, "", fmt.Errorf("traffic reset day must be configured before calibration")
	}
	start := schedule.Last(now)
	return start, schedule.CycleKey(now), nil
}

func LoadCalibrationSnapshot(ctx context.Context, db *gorm.DB, client models.Client, now time.Time) (CalibrationSnapshot, error) {
	days, snapshot, err := loadCalibrationDays(ctx, db, client, now)
	if err != nil {
		return CalibrationSnapshot{}, err
	}
	for _, day := range days {
		snapshot.Raw = addUsage(snapshot.Raw, day.Raw)
		snapshot.Adjustment = addSignedUsage(snapshot.Adjustment, day.Adjustment)
		snapshot.Effective = addUsage(snapshot.Effective, day.Effective)
	}
	history, err := loadCalibrationHistory(ctx, db, client.UUID, snapshot.Cycle)
	if err != nil {
		return CalibrationSnapshot{}, err
	}
	snapshot.History = history
	return snapshot, nil
}

// calibrationDaysByKey walks Beijing calendar days from the cycle start through
// today. Walking the vendor reset instant with AddDate skips today when that
// instant is later the same Beijing day (for example UTC 12:38 after 00:00 CST).
func calibrationDaysByKey(cycleStart, today time.Time) (map[string]*calibrationDay, []string) {
	today = BeijingDay(today)
	startDay := BeijingDay(cycleStart)
	if startDay.IsZero() || startDay.After(today) {
		startDay = today
	}
	n := dayCount(startDay, today.AddDate(0, 0, 1))
	if n < 1 {
		n = 1
	}
	daysByKey := make(map[string]*calibrationDay, n)
	dayKeys := make([]string, 0, n)
	for day := startDay; !day.After(today); day = day.AddDate(0, 0, 1) {
		key := dayKey(day)
		daysByKey[key] = &calibrationDay{Day: key}
		dayKeys = append(dayKeys, key)
	}
	return daysByKey, dayKeys
}

func applyCurrentDayUsage(daysByKey map[string]*calibrationDay, dayKeys []string, today time.Time, current Usage) []string {
	key := dayKey(today)
	day := daysByKey[key]
	if day == nil {
		day = &calibrationDay{Day: key}
		daysByKey[key] = day
		dayKeys = append(dayKeys, key)
	}
	day.Raw = current
	return dayKeys
}

func cycleEndDay(client models.Client, cycleStart time.Time) time.Time {
	schedule := clientSchedule(client)
	if !schedule.Active() {
		schedule = scheduleForDay(NormalizedResetDay(client.TrafficResetDay))
	}
	return cycleClosedEnd(schedule, cycleStart)
}

func cycleFirstDayWindow(cycleStart time.Time) (start, end, startDay time.Time, ok bool) {
	if isBeijingMidnight(cycleStart) {
		return time.Time{}, time.Time{}, time.Time{}, false
	}
	startDay = BeijingDay(cycleStart)
	return cycleStart, startDay.AddDate(0, 0, 1).Add(-time.Nanosecond), startDay, true
}

func loadStoredCycleFirstDay(ctx context.Context, db *gorm.DB, clientID, cycle string) (models.TrafficCycleFirstDay, bool, error) {
	var row models.TrafficCycleFirstDay
	err := db.WithContext(ctx).Where("client = ? AND cycle = ?", clientID, cycle).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.TrafficCycleFirstDay{}, false, nil
	}
	if err != nil {
		return models.TrafficCycleFirstDay{}, false, fmt.Errorf("read stored first-day traffic: %w", err)
	}
	return row, true, nil
}

func upsertCycleFirstDay(ctx context.Context, db *gorm.DB, row models.TrafficCycleFirstDay) error {
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "client"}, {Name: "cycle"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"day", "up_bytes", "down_bytes", "covered_until", "sealed", "recovered", "updated_at",
		}),
	}).Create(&row).Error
}

func firstDayHistoryStillRetained(cycleStart, now time.Time) bool {
	if cycleStart.IsZero() || now.Before(cycleStart) {
		return false
	}
	return !now.After(cycleStart.Add(24 * time.Hour))
}

func firstDayUsage(row models.TrafficCycleFirstDay) Usage {
	return Usage{Up: row.UpBytes, Down: row.DownBytes}
}

// syncCycleFirstDay writes confirmed first-day traffic while that Beijing day
// is still open, then seals the row after the day ends. A missing row after
// the first day has ended is treated as unavailable history, not a real zero.
func syncCycleFirstDay(ctx context.Context, db *gorm.DB, client models.Client, now time.Time) (Usage, bool, error) {
	cycleStart, cycle, err := CurrentTrafficCycleFor(client, now)
	if err != nil {
		return Usage{}, false, nil
	}
	_, end, startDay, ok := cycleFirstDayWindow(cycleStart)
	if !ok {
		return Usage{}, false, nil
	}
	row, found, err := loadStoredCycleFirstDay(ctx, db, client.UUID, cycle)
	if err != nil {
		return Usage{}, false, err
	}
	if found && (row.Sealed || row.Recovered) {
		return firstDayUsage(row), true, nil
	}

	firstDayEnded := startDay.Before(BeijingDay(now))
	if firstDayEnded && !found && !firstDayHistoryStillRetained(cycleStart, now) {
		return Usage{}, false, nil
	}

	covered := cycleStart
	if found && !row.CoveredUntil.IsZero() {
		covered = row.CoveredUntil
	}
	to := now
	if to.After(end) {
		to = end
	}
	if !covered.After(to) {
		extra, err := metricUsageForCalibration(ctx, client.UUID, covered.UTC(), to.UTC())
		if err != nil {
			return Usage{}, false, err
		}
		if found {
			row.UpBytes = saturatingAdd(row.UpBytes, extra.Up)
			row.DownBytes = saturatingAdd(row.DownBytes, extra.Down)
		} else {
			row = models.TrafficCycleFirstDay{
				Client: client.UUID, Cycle: cycle, Day: dayKey(startDay),
				UpBytes: extra.Up, DownBytes: extra.Down,
			}
		}
		row.CoveredUntil = to.Add(time.Nanosecond)
	} else if !found {
		return Usage{}, false, nil
	}

	row.Client = client.UUID
	row.Cycle = cycle
	row.Day = dayKey(startDay)
	row.Sealed = firstDayEnded || !now.Before(end.Add(time.Nanosecond))
	if err := upsertCycleFirstDay(ctx, db, row); err != nil {
		return Usage{}, false, fmt.Errorf("persist first-day traffic: %w", err)
	}
	return firstDayUsage(row), true, nil
}

func persistCycleFirstDays(ctx context.Context, db *gorm.DB, now time.Time) error {
	var clients []models.Client
	if err := db.WithContext(ctx).
		Select("uuid", "traffic_reset_day", "traffic_reset_time", "traffic_reset_timezone").
		Find(&clients).Error; err != nil {
		return fmt.Errorf("list clients for first-day traffic: %w", err)
	}
	for _, client := range clients {
		if _, _, err := syncCycleFirstDay(ctx, db, client, now); err != nil {
			return fmt.Errorf("settle first-day traffic for client %s: %w", client.UUID, err)
		}
	}
	return nil
}

func markCycleFirstDayRecovered(ctx context.Context, db *gorm.DB, client models.Client, now time.Time) error {
	cycleStart, cycle, err := CurrentTrafficCycleFor(client, now)
	if err != nil {
		return err
	}
	_, end, startDay, ok := cycleFirstDayWindow(cycleStart)
	if !ok {
		return nil
	}
	row, found, err := loadStoredCycleFirstDay(ctx, db, client.UUID, cycle)
	if err != nil {
		return err
	}
	if !found {
		row = models.TrafficCycleFirstDay{
			Client: client.UUID, Cycle: cycle, Day: dayKey(startDay),
		}
	}
	if row.CoveredUntil.IsZero() && !end.IsZero() {
		row.CoveredUntil = end.Add(time.Nanosecond)
	}
	row.Client = client.UUID
	row.Cycle = cycle
	row.Day = dayKey(startDay)
	row.Sealed = true
	row.Recovered = true
	if err := upsertCycleFirstDay(ctx, db, row); err != nil {
		return fmt.Errorf("mark first-day traffic recovered: %w", err)
	}
	return nil
}

func loadCalibrationDays(ctx context.Context, db *gorm.DB, client models.Client, now time.Time) ([]calibrationDay, CalibrationSnapshot, error) {
	cycleStart, cycle, err := CurrentTrafficCycleFor(client, now)
	if err != nil {
		return nil, CalibrationSnapshot{}, err
	}
	today := BeijingDay(now)
	startDay := BeijingDay(cycleStart)
	if startDay.IsZero() || startDay.After(today) {
		startDay = today
	}

	daysByKey, dayKeys := calibrationDaysByKey(cycleStart, today)

	ledgerStart := startDay
	if !isBeijingMidnight(cycleStart) {
		ledgerStart = startDay.AddDate(0, 0, 1)
	}
	if ledgerStart.Before(today) {
		if err := EnsureRange(ctx, db, []string{client.UUID}, ledgerStart, today); err != nil {
			return nil, CalibrationSnapshot{}, fmt.Errorf("settle traffic before calibration: %w", err)
		}
		var rows []models.TrafficDailyLedger
		if err := db.WithContext(ctx).
			Select("day", "up_bytes", "down_bytes").
			Where("client = ? AND day >= ? AND day < ?", client.UUID, dayKey(ledgerStart), dayKey(today)).
			Find(&rows).Error; err != nil {
			return nil, CalibrationSnapshot{}, fmt.Errorf("read settled traffic before calibration: %w", err)
		}
		for _, row := range rows {
			if day := daysByKey[row.Day]; day != nil {
				day.Raw = Usage{Up: row.UpBytes, Down: row.DownBytes}
			}
		}
	}

	historyComplete := true
	if !isBeijingMidnight(cycleStart) && startDay.Before(today) {
		first, available, err := syncCycleFirstDay(ctx, db, client, now)
		if err != nil {
			return nil, CalibrationSnapshot{}, fmt.Errorf("read first-day traffic before calibration: %w", err)
		}
		if available {
			dayKeys = applyCurrentDayUsage(daysByKey, dayKeys, startDay, first)
		} else {
			historyComplete = false
		}
	}

	currentStart := today
	if cycleStart.After(currentStart) {
		currentStart = cycleStart
	}
	current, err := metricUsageForCalibration(ctx, client.UUID, currentStart.UTC(), now.UTC())
	if err != nil {
		return nil, CalibrationSnapshot{}, fmt.Errorf("read current traffic before calibration: %w", err)
	}
	dayKeys = applyCurrentDayUsage(daysByKey, dayKeys, today, current)

	var adjustments []struct {
		Day       string
		UpDelta   int64
		DownDelta int64
	}
	if err := db.WithContext(ctx).Model(&models.TrafficCalibrationAdjustment{}).
		Select("day, SUM(up_delta) AS up_delta, SUM(down_delta) AS down_delta").
		Where("client = ? AND cycle = ?", client.UUID, cycle).
		Group("day").
		Scan(&adjustments).Error; err != nil {
		return nil, CalibrationSnapshot{}, fmt.Errorf("read traffic calibration adjustments: %w", err)
	}
	for _, adjustment := range adjustments {
		if day := daysByKey[adjustment.Day]; day != nil {
			day.Adjustment = SignedUsage{Up: adjustment.UpDelta, Down: adjustment.DownDelta}
		}
	}

	days := make([]calibrationDay, 0, len(dayKeys))
	for _, key := range dayKeys {
		day := daysByKey[key]
		if day == nil {
			continue
		}
		day.Effective = Usage{
			Up:   addSignedNonNegative(day.Raw.Up, day.Adjustment.Up),
			Down: addSignedNonNegative(day.Raw.Down, day.Adjustment.Down),
		}
		days = append(days, *day)
	}
	return days, CalibrationSnapshot{
		Client:          client.UUID,
		Cycle:           cycle,
		CycleStart:      cycleStart.UTC(),
		CycleEnd:        cycleEndDay(client, cycleStart).UTC(),
		HistoryComplete: historyComplete,
	}, nil
}

func CalibrateCurrentCycle(ctx context.Context, db *gorm.DB, client models.Client, target Usage, operator string, now time.Time) (CalibrationSnapshot, error) {
	if target.Up < 0 || target.Down < 0 {
		return CalibrationSnapshot{}, fmt.Errorf("traffic calibration values cannot be negative")
	}
	days, snapshot, err := loadCalibrationDays(ctx, db, client, now)
	if err != nil {
		return CalibrationSnapshot{}, err
	}
	for _, day := range days {
		snapshot.Raw = addUsage(snapshot.Raw, day.Raw)
		snapshot.Adjustment = addSignedUsage(snapshot.Adjustment, day.Adjustment)
		snapshot.Effective = addUsage(snapshot.Effective, day.Effective)
	}
	upDelta := target.Up - snapshot.Effective.Up
	downDelta := target.Down - snapshot.Effective.Down
	if upDelta == 0 && downDelta == 0 {
		if !snapshot.HistoryComplete {
			if err := markCycleFirstDayRecovered(ctx, db, client, now); err != nil {
				return CalibrationSnapshot{}, err
			}
			InvalidateCalibratedCycleCache()
			return LoadCalibrationSnapshot(ctx, db, client, now)
		}
		history, historyErr := loadCalibrationHistory(ctx, db, client.UUID, snapshot.Cycle)
		if historyErr != nil {
			return CalibrationSnapshot{}, historyErr
		}
		snapshot.History = history
		return snapshot, nil
	}

	upAllocation, err := allocateCalibration(days, upDelta, func(day calibrationDay) int64 { return day.Effective.Up })
	if err != nil {
		return CalibrationSnapshot{}, err
	}
	downAllocation, err := allocateCalibration(days, downDelta, func(day calibrationDay) int64 { return day.Effective.Down })
	if err != nil {
		return CalibrationSnapshot{}, err
	}
	calibrationID, err := newCalibrationID()
	if err != nil {
		return CalibrationSnapshot{}, fmt.Errorf("generate traffic calibration id: %w", err)
	}
	createdAt := now.UTC()
	rows := make([]models.TrafficCalibrationAdjustment, 0, len(days))
	for _, day := range days {
		up := upAllocation[day.Day]
		down := downAllocation[day.Day]
		if up == 0 && down == 0 {
			continue
		}
		rows = append(rows, models.TrafficCalibrationAdjustment{
			CalibrationID: calibrationID,
			Client:        client.UUID,
			Cycle:         snapshot.Cycle,
			Day:           day.Day,
			UpDelta:       up,
			DownDelta:     down,
			TargetUp:      target.Up,
			TargetDown:    target.Down,
			Operator:      operator,
			CreatedAt:     createdAt,
		})
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
		if snapshot.HistoryComplete {
			return nil
		}
		return markCycleFirstDayRecovered(ctx, tx, client, now)
	}); err != nil {
		return CalibrationSnapshot{}, fmt.Errorf("save traffic calibration: %w", err)
	}
	InvalidateCalibratedCycleCache()
	return LoadCalibrationSnapshot(ctx, db, client, now)
}

func allocateCalibration(days []calibrationDay, delta int64, effective func(calibrationDay) int64) (map[string]int64, error) {
	allocated := make(map[string]int64)
	if delta == 0 || len(days) == 0 {
		return allocated, nil
	}
	if delta > 0 {
		allocated[days[len(days)-1].Day] = delta
		return allocated, nil
	}
	remaining := -delta
	for index := len(days) - 1; index >= 0 && remaining > 0; index-- {
		available := effective(days[index])
		if available <= 0 {
			continue
		}
		take := available
		if take > remaining {
			take = remaining
		}
		allocated[days[index].Day] = -take
		remaining -= take
	}
	if remaining != 0 {
		return nil, fmt.Errorf("traffic calibration would make historical usage negative")
	}
	return allocated, nil
}

func AdjustedMetricUsage(ctx context.Context, db *gorm.DB, clientID string, start, end time.Time) (Usage, error) {
	raw, err := MetricUsage(ctx, clientID, start, end)
	if err != nil {
		return Usage{}, err
	}
	adjustment, err := rangeAdjustment(ctx, db, clientID, BeijingDay(start), BeijingDay(end).AddDate(0, 0, 1))
	if err != nil {
		return Usage{}, err
	}
	return applySignedUsage(raw, adjustment), nil
}

func AdjustedLedgerUsage(ctx context.Context, db *gorm.DB, clientID string, startDay, endDay time.Time) (Usage, error) {
	raw, err := SumRange(ctx, db, clientID, startDay, endDay)
	if err != nil {
		return Usage{}, err
	}
	adjustment, err := rangeAdjustment(ctx, db, clientID, startDay, endDay)
	if err != nil {
		return Usage{}, err
	}
	return applySignedUsage(raw, adjustment), nil
}

func DailyAdjustments(ctx context.Context, db *gorm.DB, startDay, endDay time.Time) (map[string]SignedUsage, error) {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Client    string
		Day       string
		UpDelta   int64
		DownDelta int64
	}
	if err := db.WithContext(ctx).Model(&models.TrafficCalibrationAdjustment{}).
		Select("client, day, SUM(up_delta) AS up_delta, SUM(down_delta) AS down_delta").
		Where("day >= ? AND day < ?", dayKey(start), dayKey(end)).
		Group("client, day").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("read daily traffic adjustments: %w", err)
	}
	result := make(map[string]SignedUsage, len(rows))
	for _, row := range rows {
		result[row.Client+"\x00"+row.Day] = SignedUsage{Up: row.UpDelta, Down: row.DownDelta}
	}
	return result, nil
}

func ApplyHourlyAdjustment(hours []HourlyUsage, adjustment SignedUsage, now time.Time) []HourlyUsage {
	result := append([]HourlyUsage(nil), hours...)
	currentHour := now.In(BeijingLocation).Truncate(time.Hour)
	if len(result) == 0 {
		result = append(result, HourlyUsage{Hour: currentHour})
	}
	applyHourlyDirection(result, adjustment.Up, func(hour *HourlyUsage) *int64 { return &hour.Up })
	applyHourlyDirection(result, adjustment.Down, func(hour *HourlyUsage) *int64 { return &hour.Down })
	return result
}

func ApplyAdjustment(raw Usage, adjustment SignedUsage) Usage {
	return applySignedUsage(raw, adjustment)
}

// ShiftCumulativeCounter preserves a recent cumulative series while making
// its newest point agree with the calibrated cycle usage.
func ShiftCumulativeCounter(value, newestRaw, newestEffective int64) int64 {
	return addSignedNonNegative(value, newestEffective-newestRaw)
}

func applyHourlyDirection(hours []HourlyUsage, delta int64, field func(*HourlyUsage) *int64) {
	if len(hours) == 0 {
		return
	}
	if delta >= 0 {
		*field(&hours[len(hours)-1]) += delta
		return
	}
	remaining := -delta
	for index := len(hours) - 1; index >= 0 && remaining > 0; index-- {
		value := field(&hours[index])
		take := *value
		if take > remaining {
			take = remaining
		}
		*value -= take
		remaining -= take
	}
}

func CurrentCalibratedCycleUsages(ctx context.Context, db *gorm.DB, now time.Time) (map[string]Usage, error) {
	calibratedCycleCache.Lock()
	if now.Before(calibratedCycleCache.expires) && calibratedCycleCache.values != nil {
		values := cloneUsageMap(calibratedCycleCache.values)
		calibratedCycleCache.Unlock()
		return values, nil
	}
	calibratedCycleCache.Unlock()

	cutoff := BeijingDay(now).AddDate(0, 0, -32).Format(time.DateOnly)
	var refs []struct {
		Client string
		Cycle  string
	}
	if err := db.WithContext(ctx).Model(&models.TrafficCalibrationAdjustment{}).
		Select("client, cycle").
		Where("cycle >= ?", cutoff).
		Group("client, cycle").
		Scan(&refs).Error; err != nil {
		return nil, fmt.Errorf("list active traffic calibrations: %w", err)
	}
	if len(refs) == 0 {
		calibratedCycleCache.Lock()
		calibratedCycleCache.values = map[string]Usage{}
		calibratedCycleCache.expires = now.Add(calibratedCycleCacheTTL)
		calibratedCycleCache.Unlock()
		return map[string]Usage{}, nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.Client)
	}
	var clients []models.Client
	if err := db.WithContext(ctx).Select("uuid", "traffic_reset_day", "traffic_reset_time", "traffic_reset_timezone").Where("uuid IN ?", ids).Find(&clients).Error; err != nil {
		return nil, fmt.Errorf("load calibrated clients: %w", err)
	}
	cycles := make(map[string][]string, len(refs))
	for _, ref := range refs {
		cycles[ref.Client] = append(cycles[ref.Client], ref.Cycle)
	}
	values := make(map[string]Usage)
	for _, client := range clients {
		active := false
		for _, cycle := range cycles[client.UUID] {
			if _, current, err := CurrentTrafficCycleFor(client, now); err == nil && current == cycle {
				active = true
				break
			}
		}
		if !active {
			continue
		}
		snapshot, err := LoadCalibrationSnapshot(ctx, db, client, now)
		if err != nil || !snapshot.HistoryComplete {
			continue
		}
		values[client.UUID] = snapshot.Effective
	}
	calibratedCycleCache.Lock()
	calibratedCycleCache.values = cloneUsageMap(values)
	calibratedCycleCache.expires = now.Add(calibratedCycleCacheTTL)
	calibratedCycleCache.Unlock()
	return values, nil
}

func InvalidateCalibratedCycleCache() {
	calibratedCycleCache.Lock()
	calibratedCycleCache.expires = time.Time{}
	calibratedCycleCache.values = nil
	calibratedCycleCache.Unlock()
}

func rangeAdjustment(ctx context.Context, db *gorm.DB, clientID string, startDay, endDay time.Time) (SignedUsage, error) {
	start, end, err := normalizeRange(startDay, endDay)
	if err != nil {
		return SignedUsage{}, err
	}
	var result SignedUsage
	if err := db.WithContext(ctx).Model(&models.TrafficCalibrationAdjustment{}).
		Select("COALESCE(SUM(up_delta), 0) AS up, COALESCE(SUM(down_delta), 0) AS down").
		Where("client = ? AND day >= ? AND day < ?", clientID, dayKey(start), dayKey(end)).
		Scan(&result).Error; err != nil {
		return SignedUsage{}, fmt.Errorf("sum traffic calibration adjustments: %w", err)
	}
	return result, nil
}

func loadCalibrationHistory(ctx context.Context, db *gorm.DB, clientID, cycle string) ([]CalibrationHistory, error) {
	var rows []struct {
		CalibrationID string
		TargetUp      int64
		TargetDown    int64
		UpDelta       int64
		DownDelta     int64
		Operator      string
		CreatedAt     time.Time
	}
	if err := db.WithContext(ctx).Model(&models.TrafficCalibrationAdjustment{}).
		Select("calibration_id, target_up, target_down, operator, created_at, SUM(up_delta) AS up_delta, SUM(down_delta) AS down_delta").
		Where("client = ? AND cycle = ?", clientID, cycle).
		Group("calibration_id, target_up, target_down, operator, created_at").
		Order("created_at DESC").
		Limit(10).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("read traffic calibration history: %w", err)
	}
	history := make([]CalibrationHistory, 0, len(rows))
	for _, row := range rows {
		history = append(history, CalibrationHistory{
			CalibrationID: row.CalibrationID,
			Target:        Usage{Up: row.TargetUp, Down: row.TargetDown},
			Adjustment:    SignedUsage{Up: row.UpDelta, Down: row.DownDelta},
			Operator:      row.Operator,
			CreatedAt:     row.CreatedAt.UTC(),
		})
	}
	return history, nil
}

func newCalibrationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func addUsage(left, right Usage) Usage {
	return Usage{Up: saturatingAdd(left.Up, right.Up), Down: saturatingAdd(left.Down, right.Down)}
}

func addSignedUsage(left, right SignedUsage) SignedUsage {
	return SignedUsage{Up: saturatingAddSigned(left.Up, right.Up), Down: saturatingAddSigned(left.Down, right.Down)}
}

func applySignedUsage(raw Usage, adjustment SignedUsage) Usage {
	return Usage{
		Up:   addSignedNonNegative(raw.Up, adjustment.Up),
		Down: addSignedNonNegative(raw.Down, adjustment.Down),
	}
}

func addSignedNonNegative(value, delta int64) int64 {
	if delta >= 0 {
		return saturatingAdd(value, delta)
	}
	if delta <= -value {
		return 0
	}
	return value + delta
}

func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func saturatingAddSigned(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func cloneUsageMap(source map[string]Usage) map[string]Usage {
	result := make(map[string]Usage, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
