package metricstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	logger "github.com/nuomiiiii/lite/utils/log"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/metric"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

type reportTrafficState struct {
	mu sync.Mutex

	reportTrafficValues
}

type reportTrafficValues struct {
	initialized bool
	timestamp   time.Time
	hasUp       bool
	totalUp     int64
	hasDown     bool
	totalDown   int64
	hasUptime   bool
	uptime      int64
}

var reportTrafficStates sync.Map

const (
	reportBatchInterval         = 3 * time.Second
	reportBatchQueueSize        = 4096
	pingBatchQueueSize          = 4096
	reportBatchWriteTimeout     = 10 * time.Second
	reportTrafficRateMultiplier = int64(4)
	reportTrafficRateAllowance  = int64(64 * 1024 * 1024)
)

var (
	reportBatcherMu sync.Mutex
	reportBatcher   *reportBatchWorker
)

var (
	ErrReportBatchStopped = errors.New("metric report batcher is stopped")
)

type reportBatchRequest struct {
	ctx  context.Context
	done chan error
	stop bool
}

type pingBatchItem struct {
	rec  models.PingRecord
	done chan error
}

type reportBatchWorker struct {
	mu             sync.Mutex
	queue          chan v2.Report
	pingQueue      chan pingBatchItem
	requests       chan reportBatchRequest
	flushNudge     chan struct{}
	done           chan struct{}
	reportInflight atomic.Int64
	pingInflight   atomic.Int64
	stopping       bool
}

// StartReportBatcher starts the shared report writer.
func StartReportBatcher() {
	reportBatcherMu.Lock()
	defer reportBatcherMu.Unlock()
	if reportBatcher != nil {
		return
	}
	worker := &reportBatchWorker{
		queue:      make(chan v2.Report, reportBatchQueueSize),
		pingQueue:  make(chan pingBatchItem, pingBatchQueueSize),
		requests:   make(chan reportBatchRequest, 1),
		flushNudge: make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	reportBatcher = worker
	go worker.run()
}

// StopReportBatcher stops the report writer after flushing all queued reports.
func StopReportBatcher(ctx context.Context) error {
	reportBatcherMu.Lock()
	worker := reportBatcher
	if worker == nil {
		reportBatcherMu.Unlock()
		return nil
	}
	worker.mu.Lock()
	worker.stopping = true
	worker.mu.Unlock()
	reportBatcherMu.Unlock()

	request := reportBatchRequest{ctx: ctx, done: make(chan error, 1), stop: true}
	select {
	case worker.requests <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.done:
		<-worker.done
		reportBatcherMu.Lock()
		if reportBatcher == worker {
			reportBatcher = nil
		}
		reportBatcherMu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FlushReportBatch synchronously flushes queued load reports. Ping records are
// written as soon as they are queued and are not delayed for this flush.
func FlushReportBatch(ctx context.Context) error {
	reportBatcherMu.Lock()
	worker := reportBatcher
	reportBatcherMu.Unlock()
	if worker == nil {
		return nil
	}
	request := reportBatchRequest{ctx: ctx, done: make(chan error, 1)}
	worker.mu.Lock()
	stopping := worker.stopping
	worker.mu.Unlock()
	if stopping {
		return ErrReportBatchStopped
	}
	select {
	case worker.requests <- request:
	case <-worker.done:
		return ErrReportBatchStopped
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WriteReport persists one agent report as raw metric points sharing the same
// server receive time. Traffic deltas are derived from the previous counters
// and stored alongside the raw counters so they remain summable after rollup.
func WriteReport(ctx context.Context, report v2.Report) (v2.Report, error) {
	if report.UUID == "" {
		return v2.Report{}, fmt.Errorf("report UUID is required")
	}
	if report.UpdatedAt.IsZero() {
		return v2.Report{}, fmt.Errorf("report receive time is required")
	}
	if EntityWritesBlocked(report.UUID) {
		return v2.Report{}, ErrMetricWriteBlocked
	}
	report.UpdatedAt = report.UpdatedAt.UTC()
	if GetStore() == nil {
		return v2.Report{}, fmt.Errorf("metric store not enabled")
	}

	reportBatcherMu.Lock()
	worker := reportBatcher
	reportBatcherMu.Unlock()
	if worker != nil {
		if err := worker.enqueueReport(ctx, report); err != nil {
			return v2.Report{}, err
		}
		return report, nil
	}

	saved, err := writeReportBatch(ctx, []v2.Report{report})
	if err != nil {
		return v2.Report{}, err
	}
	if len(saved) == 0 {
		return v2.Report{}, ErrMetricWriteBlocked
	}
	return saved[0], nil
}

func (w *reportBatchWorker) enqueueReport(ctx context.Context, report v2.Report) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	select {
	case <-w.done:
		w.mu.Unlock()
		_, err := writeReportBatch(ctx, []v2.Report{report})
		return err
	default:
	}
	w.reportInflight.Add(1)
	w.mu.Unlock()
	defer w.reportInflight.Add(-1)
	select {
	case w.queue <- report:
		if len(w.queue)*4 >= cap(w.queue)*3 {
			w.nudgeFlush()
		}
		return nil
	default:
	}
	w.nudgeFlush()
	select {
	case w.queue <- report:
		return nil
	case <-w.done:
		_, err := writeReportBatch(ctx, []v2.Report{report})
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *reportBatchWorker) enqueuePing(ctx context.Context, rec models.PingRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	select {
	case <-w.done:
		w.mu.Unlock()
		return writePingRecords(ctx, []models.PingRecord{rec})
	default:
	}
	w.pingInflight.Add(1)
	w.mu.Unlock()
	item := pingBatchItem{rec: rec, done: make(chan error, 1)}
	select {
	case w.pingQueue <- item:
		w.pingInflight.Add(-1)
	case <-w.done:
		w.pingInflight.Add(-1)
		return writePingRecords(ctx, []models.PingRecord{rec})
	case <-ctx.Done():
		w.pingInflight.Add(-1)
		return ctx.Err()
	}
	select {
	case err := <-item.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *reportBatchWorker) nudgeFlush() {
	select {
	case w.flushNudge <- struct{}{}:
	default:
	}
}

func (w *reportBatchWorker) run() {
	ticker := time.NewTicker(reportBatchInterval)
	defer ticker.Stop()

	var pending []v2.Report
	flushReports := func(ctx context.Context) error {
		pending = append(pending, drainReportQueue(w.queue, cap(w.queue))...)
		return writePendingReports(ctx, &pending)
	}
	flushPings := func(ctx context.Context, first pingBatchItem, hasFirst bool) {
		items := drainPingQueue(w.pingQueue, cap(w.pingQueue))
		if hasFirst {
			batch := make([]pingBatchItem, 0, 1+len(items))
			batch = append(batch, first)
			items = append(batch, items...)
		}
		if len(items) == 0 {
			return
		}
		writable := items[:0]
		for _, item := range items {
			assignment := PingAssignment{Client: item.rec.Client, TaskID: item.rec.TaskId}
			if EntityWritesBlocked(item.rec.Client) || PingTaskWritesBlocked(item.rec.TaskId) || PingAssignmentWritesBlocked(assignment) {
				item.done <- ErrMetricWriteBlocked
				continue
			}
			writable = append(writable, item)
		}
		if len(writable) == 0 {
			return
		}
		records := make([]models.PingRecord, len(writable))
		for i, item := range writable {
			records[i] = item.rec
		}
		err := writePingRecords(ctx, records)
		for _, item := range writable {
			item.done <- err
		}
	}

	for {
		select {
		case request := <-w.requests:
			if !request.stop {
				flushPings(request.ctx, pingBatchItem{}, false)
				request.done <- flushReports(request.ctx)
				continue
			}
			var err error
			for {
				for w.pingInflight.Load() > 0 || len(w.pingQueue) > 0 {
					select {
					case item := <-w.pingQueue:
						flushPings(request.ctx, item, true)
					default:
						flushPings(request.ctx, pingBatchItem{}, false)
						if w.pingInflight.Load() > 0 && len(w.pingQueue) == 0 {
							runtime.Gosched()
						}
					}
				}
				if ferr := flushReports(request.ctx); ferr != nil && err == nil {
					err = ferr
				}
				for w.reportInflight.Load() > 0 || len(w.queue) > 0 {
					if ferr := flushReports(request.ctx); ferr != nil {
						if err == nil {
							err = ferr
						}
						break
					}
					if w.reportInflight.Load() > 0 && len(w.queue) == 0 {
						runtime.Gosched()
					}
				}
				w.mu.Lock()
				idle := w.pingInflight.Load() == 0 && len(w.pingQueue) == 0 &&
					w.reportInflight.Load() == 0 && len(w.queue) == 0
				if idle {
					close(w.done)
					w.mu.Unlock()
					break
				}
				w.mu.Unlock()
				runtime.Gosched()
			}
			if err != nil {
				logger.Errorf("metricstore", "failed to flush metric report batch during shutdown: %v", err)
			}
			request.done <- err
			return
		case <-w.flushNudge:
			if err := flushReports(context.Background()); err != nil {
				logger.Errorf("metricstore", "failed to flush metric report batch: %v", err)
			}
		case <-ticker.C:
			if err := flushReports(context.Background()); err != nil {
				logger.Errorf("metricstore", "failed to flush metric report batch: %v", err)
			}
		case item := <-w.pingQueue:
			flushPings(context.Background(), item, true)
		}
	}
}

func drainPingQueue(queue <-chan pingBatchItem, limit int) []pingBatchItem {
	if limit <= 0 {
		return nil
	}
	capacity := len(queue)
	if capacity == 0 {
		return nil
	}
	if capacity > limit {
		capacity = limit
	}
	items := make([]pingBatchItem, 0, capacity)
	for len(items) < limit {
		select {
		case item := <-queue:
			items = append(items, item)
		default:
			return items
		}
	}
	return items
}

func drainReportQueue(queue <-chan v2.Report, limit int) []v2.Report {
	if limit <= 0 {
		return nil
	}
	capacity := len(queue)
	if capacity == 0 {
		return nil
	}
	if capacity > limit {
		capacity = limit
	}
	reports := make([]v2.Report, 0, capacity)
	for len(reports) < limit {
		select {
		case report := <-queue:
			reports = append(reports, report)
		default:
			return reports
		}
	}
	return reports
}

func writePendingReports(ctx context.Context, pending *[]v2.Report) error {
	if len(*pending) == 0 {
		return nil
	}
	batchSize := len(*pending)
	for len(*pending) > 0 {
		if batchSize > len(*pending) {
			batchSize = len(*pending)
		}
		writeCtx, cancel := context.WithTimeout(ctx, reportBatchWriteTimeout)
		_, err := writeReportBatch(writeCtx, (*pending)[:batchSize])
		cancel()
		if err != nil {
			return err
		}
		*pending = (*pending)[batchSize:]
	}
	return nil
}

func writeReportBatch(ctx context.Context, reports []v2.Report) ([]v2.Report, error) {
	if len(reports) == 0 {
		return nil, nil
	}
	filtered := reports[:0]
	for _, report := range reports {
		if !EntityWritesBlocked(report.UUID) {
			filtered = append(filtered, report)
		}
	}
	reports = filtered
	if len(reports) == 0 {
		return nil, nil
	}
	if err := storeOperations.AcquireShared(ctx); err != nil {
		return nil, fmt.Errorf("wait for metric store operation before writing reports: %w", err)
	}
	defer storeOperations.ReleaseShared()
	filtered = reports[:0]
	for _, report := range reports {
		if !EntityWritesBlocked(report.UUID) {
			filtered = append(filtered, report)
		}
	}
	reports = filtered
	if len(reports) == 0 {
		return nil, nil
	}

	s := GetStore()
	if s == nil {
		return nil, fmt.Errorf("metric store not enabled")
	}

	prepared := make([]v2.Report, len(reports))
	copy(prepared, reports)
	points := make([]metric.Point, 0, len(reports)*20)
	pendingStates := make(map[*reportTrafficState]reportTrafficValues)
	for i, report := range prepared {
		stateValue, _ := reportTrafficStates.LoadOrStore(report.UUID, &reportTrafficState{})
		state := stateValue.(*reportTrafficState)
		values, ok := pendingStates[state]
		if !ok {
			state.mu.Lock()
			values = state.reportTrafficValues
			state.mu.Unlock()
		}
		if !values.initialized {
			totalUp, upTime, hasUp, err := latestReportCounter(ctx, s, MetricNetTotalUp, report.UUID, report.UpdatedAt)
			if err == nil {
				values.totalUp = totalUp
				values.hasUp = hasUp
				if hasUp && upTime.After(values.timestamp) {
					values.timestamp = upTime
				}
			} else if ctx.Err() == nil {
				logger.Errorf("metricstore", "failed to restore previous upload counter for %s: %v", report.UUID, err)
			}
			totalDown, downTime, hasDown, err := latestReportCounter(ctx, s, MetricNetTotalDown, report.UUID, report.UpdatedAt)
			if err == nil {
				values.totalDown = totalDown
				values.hasDown = hasDown
				if hasDown && downTime.After(values.timestamp) {
					values.timestamp = downTime
				}
			} else if ctx.Err() == nil {
				logger.Errorf("metricstore", "failed to restore previous download counter for %s: %v", report.UUID, err)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			values.initialized = true
		}

		if !values.timestamp.IsZero() && !report.UpdatedAt.After(values.timestamp) {
			report.UpdatedAt = values.timestamp.Add(time.Nanosecond)
		}
		elapsed := report.UpdatedAt.Sub(values.timestamp)
		agentRestart := values.hasUptime && report.Uptime < values.uptime
		trafficUp := int64(0)
		if values.hasUp && !agentRestart {
			trafficUp = ReportTrafficCounterDelta(report.Network.TotalUp, values.totalUp, report.Network.Up, elapsed)
		}
		trafficDown := int64(0)
		if values.hasDown && !agentRestart {
			trafficDown = ReportTrafficCounterDelta(report.Network.TotalDown, values.totalDown, report.Network.Down, elapsed)
		}
		points = append(points, reportMetricPoints(report, trafficUp, trafficDown)...)
		values.timestamp = report.UpdatedAt
		values.hasUp = true
		values.totalUp = report.Network.TotalUp
		values.hasDown = true
		values.totalDown = report.Network.TotalDown
		values.hasUptime = true
		values.uptime = report.Uptime
		pendingStates[state] = values
		prepared[i] = report
	}

	if err := s.WriteBatch(ctx, points); err != nil {
		return nil, err
	}
	for state, values := range pendingStates {
		state.mu.Lock()
		state.reportTrafficValues = values
		state.mu.Unlock()
	}
	return prepared, nil
}

func reportMetricPoints(report v2.Report, trafficUp, trafficDown int64) []metric.Point {
	entityID := report.UUID
	ts := report.UpdatedAt
	points := []metric.Point{
		{MetricName: MetricCPU, EntityID: entityID, Timestamp: ts, Value: report.CPU.Usage},
		{MetricName: MetricRAM, EntityID: entityID, Timestamp: ts, Value: float64(report.Ram.Used)},
		{MetricName: MetricSwap, EntityID: entityID, Timestamp: ts, Value: float64(report.Swap.Used)},
		{MetricName: MetricLoad, EntityID: entityID, Timestamp: ts, Value: report.Load.Load1},
		{MetricName: MetricDisk, EntityID: entityID, Timestamp: ts, Value: float64(report.Disk.Used)},
		{MetricName: MetricNetIn, EntityID: entityID, Timestamp: ts, Value: float64(report.Network.Down)},
		{MetricName: MetricNetOut, EntityID: entityID, Timestamp: ts, Value: float64(report.Network.Up)},
		{MetricName: MetricNetTotalUp, EntityID: entityID, Timestamp: ts, Value: float64(report.Network.TotalUp)},
		{MetricName: MetricNetTotalDown, EntityID: entityID, Timestamp: ts, Value: float64(report.Network.TotalDown)},
		{MetricName: MetricTrafficUp, EntityID: entityID, Timestamp: ts, Value: float64(trafficUp)},
		{MetricName: MetricTrafficDown, EntityID: entityID, Timestamp: ts, Value: float64(trafficDown)},
		{MetricName: MetricProcess, EntityID: entityID, Timestamp: ts, Value: float64(report.Process)},
		{MetricName: MetricConnections, EntityID: entityID, Timestamp: ts, Value: float64(report.Connections.TCP)},
		{MetricName: MetricConnectionsUDP, EntityID: entityID, Timestamp: ts, Value: float64(report.Connections.UDP)},
	}
	if report.GPU == nil {
		return points
	}
	points = append(points, metric.Point{MetricName: MetricGPU, EntityID: entityID, Timestamp: ts, Value: report.GPU.AverageUsage})
	for deviceIndex, gpu := range report.GPU.DetailedInfo {
		tags := map[string]string{
			"device_index": strconv.Itoa(deviceIndex),
			"device_name":  gpu.Name,
		}
		points = append(points,
			metric.Point{MetricName: MetricGPUMem, EntityID: entityID, Timestamp: ts, Value: float64(gpu.MemoryUsed), Tags: tags},
			metric.Point{MetricName: MetricGPUMemTotal, EntityID: entityID, Timestamp: ts, Value: float64(gpu.MemoryTotal), Tags: tags},
			metric.Point{MetricName: MetricGPUDeviceUsage, EntityID: entityID, Timestamp: ts, Value: gpu.Utilization, Tags: tags},
			metric.Point{MetricName: MetricGPUTemp, EntityID: entityID, Timestamp: ts, Value: float64(gpu.Temperature), Tags: tags},
		)
	}
	return points
}

func latestReportCounter(ctx context.Context, s *metric.Store, metricName, entityID string, before time.Time) (int64, time.Time, bool, error) {
	point, ok, err := s.LatestBefore(ctx, metricName, entityID, before)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	if !ok {
		return 0, time.Time{}, false, nil
	}
	return int64(point.Value), point.Timestamp.UTC(), true, nil
}

// GetLatestTrafficBefore returns the latest retained upload/download counters
// before a boundary, transparently reading raw points or rollups.
func GetLatestTrafficBefore(ctx context.Context, entityIDs []string, before time.Time) (map[string]models.Record, error) {
	s := GetStore()
	if s == nil {
		return nil, fmt.Errorf("metric store not enabled")
	}
	result := make(map[string]models.Record, len(entityIDs))
	for _, entityID := range entityIDs {
		if entityID == "" {
			continue
		}
		up, _, hasUp, err := latestReportCounter(ctx, s, MetricNetTotalUp, entityID, before)
		if err != nil {
			return nil, err
		}
		down, _, hasDown, err := latestReportCounter(ctx, s, MetricNetTotalDown, entityID, before)
		if err != nil {
			return nil, err
		}
		if !hasUp && !hasDown {
			continue
		}
		result[entityID] = models.Record{
			Client:       entityID,
			Time:         before.UTC().Add(-time.Nanosecond),
			NetTotalUp:   up,
			NetTotalDown: down,
		}
	}
	return result, nil
}

// TrafficCounterDelta returns the increase between two counters. A decrease
// starts a new continuity epoch; the first value in that epoch is a baseline,
// not traffic that can safely be attributed to the current report interval.
func TrafficCounterDelta(current, previous int64) int64 {
	if current < 0 || previous < 0 {
		return 0
	}
	if current >= previous {
		return current - previous
	}
	return 0
}

// ReportTrafficCounterDelta rejects positive jumps that cannot be explained by
// the Agent's measured transfer rate. This protects interface additions and
// Agent restarts without imposing a fixed link-speed limit.
func ReportTrafficCounterDelta(current, previous, bytesPerSecond int64, elapsed time.Duration) int64 {
	delta := TrafficCounterDelta(current, previous)
	if delta == 0 || elapsed <= 0 {
		return delta
	}
	if delta > reportTrafficDeltaUpperBound(bytesPerSecond, elapsed) {
		return 0
	}
	return delta
}

func reportTrafficDeltaUpperBound(bytesPerSecond int64, elapsed time.Duration) int64 {
	if bytesPerSecond < 0 {
		return reportTrafficRateAllowance
	}
	seconds := int64((elapsed + time.Second - 1) / time.Second)
	if seconds <= 0 || bytesPerSecond == 0 {
		return reportTrafficRateAllowance
	}
	if bytesPerSecond > math.MaxInt64/seconds {
		return math.MaxInt64
	}
	expected := bytesPerSecond * seconds
	if expected > (math.MaxInt64-reportTrafficRateAllowance)/reportTrafficRateMultiplier {
		return math.MaxInt64
	}
	return expected*reportTrafficRateMultiplier + reportTrafficRateAllowance
}

func deleteReportTrafficState(entityID string) {
	reportTrafficStates.Delete(entityID)
}

func clearReportTrafficStates() {
	reportTrafficStates.Range(func(key, _ any) bool {
		reportTrafficStates.Delete(key)
		return true
	})
}
