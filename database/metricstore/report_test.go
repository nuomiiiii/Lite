package metricstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/pkg/metric"
	v1 "github.com/komari-monitor/komari/protocol/v1"
)

func useReportTestStore(t *testing.T, policy *metric.RollupPolicy) *metric.Store {
	t.Helper()
	ctx := context.Background()
	opts := []metric.Option{metric.WithMaxOpenConns(1)}
	if policy != nil {
		opts = append(opts, metric.WithRollupPolicy(*policy))
	}
	dsn := fmt.Sprintf("file:report-%d?mode=memory&cache=shared", time.Now().UnixNano())
	s, err := metric.Open(ctx, metric.SQLite(dsn, opts...))
	if err != nil {
		t.Fatalf("open metric store: %v", err)
	}
	if err := createMetricDefinitions(ctx, s); err != nil {
		_ = s.Close()
		t.Fatalf("create metric definitions: %v", err)
	}

	storeMu.Lock()
	previous := store
	store = s
	storeMu.Unlock()
	clearReportTrafficStates()
	t.Cleanup(func() {
		clearReportTrafficStates()
		storeMu.Lock()
		store = previous
		storeMu.Unlock()
		_ = s.Close()
	})
	return s
}

func TestWriteReportStoresRawMetricsAndResetAwareTraffic(t *testing.T) {
	ctx := context.Background()
	policy := defaultRollupPolicy()
	s := useReportTestStore(t, &policy)
	now := time.Now().UTC().Truncate(time.Minute)
	base := now.Add(-30 * time.Minute)

	report := v1.Report{
		UUID:        "node-a",
		UpdatedAt:   base,
		CPU:         v1.CPUReport{Usage: 12.5},
		Ram:         v1.RamReport{Used: 100, Total: 1000},
		Swap:        v1.RamReport{Used: 20, Total: 200},
		Load:        v1.LoadReport{Load1: 0.5},
		Disk:        v1.DiskReport{Used: 300, Total: 3000},
		Network:     v1.NetworkReport{Up: 3, Down: 4, TotalUp: 100, TotalDown: 200},
		Process:     7,
		Connections: v1.ConnectionsReport{TCP: 8, UDP: 9},
		GPU: &v1.GPUDetailReport{
			AverageUsage: 25,
			DetailedInfo: []v1.GPUDeviceInfo{{
				Name: "GPU 0", MemoryUsed: 400, MemoryTotal: 800, Utilization: 30, Temperature: 55,
			}},
		},
	}
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write first report: %v", err)
	}

	report.UpdatedAt = base.Add(3 * time.Second)
	report.Network.TotalUp = 150
	report.Network.TotalDown = 260
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write second report: %v", err)
	}

	report.UpdatedAt = base.Add(6 * time.Second)
	report.Network.TotalUp = 20
	report.Network.TotalDown = 30
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write reset report: %v", err)
	}

	assertMetricValues(t, s, MetricTrafficUp, report.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 50, 20})
	assertMetricValues(t, s, MetricTrafficDown, report.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 60, 30})
	assertMetricValues(t, s, MetricNetTotalUp, report.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{100, 150, 20})

	gpuPoints, err := s.Query(ctx, metric.Query{
		MetricName: MetricGPUDeviceUsage,
		EntityID:   report.UUID,
		Start:      base.Add(-time.Second),
		End:        base.Add(time.Minute),
		Tags:       map[string]string{"device_index": "0"},
		Order:      metric.OrderAsc,
	})
	if err != nil {
		t.Fatalf("query GPU points: %v", err)
	}
	if len(gpuPoints) != 3 || gpuPoints[0].Timestamp != base || gpuPoints[0].Tags["device_name"] != "GPU 0" {
		t.Fatalf("unexpected GPU points: %#v", gpuPoints)
	}

	if _, err := s.Compact(ctx, now); err != nil {
		t.Fatalf("compact reports: %v", err)
	}
	deleteReportTrafficState(report.UUID)
	report.UpdatedAt = now
	report.Network.TotalUp = 35
	report.Network.TotalDown = 50
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write after restoring rollup baseline: %v", err)
	}
	assertMetricValues(t, s, MetricTrafficUp, report.UUID, now.Add(-time.Second), now.Add(time.Second), []float64{15})
	assertMetricValues(t, s, MetricTrafficDown, report.UUID, now.Add(-time.Second), now.Add(time.Second), []float64{20})
}

func TestWriteReportSkipsMetricsWithoutAgentData(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	timestamp := time.Now().UTC()
	if _, err := WriteReport(ctx, v1.Report{
		UUID: "node-without-gpu", UpdatedAt: timestamp,
	}); err != nil {
		t.Fatalf("write report: %v", err)
	}
	points, err := s.Query(ctx, metric.Query{
		MetricName: MetricGPU, EntityID: "node-without-gpu",
		Start: timestamp.Add(-time.Second), End: timestamp.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("query GPU metric: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("GPU metric was written without GPU data: %#v", points)
	}
}

func TestReportBatcherFlushesQueuedReports(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	StartReportBatcher()
	t.Cleanup(func() {
		if err := StopReportBatcher(ctx); err != nil {
			t.Errorf("stop report batcher: %v", err)
		}
	})

	base := time.Now().UTC().Truncate(time.Second)
	first := v1.Report{
		UUID:      "batched-node",
		UpdatedAt: base,
		CPU:       v1.CPUReport{Usage: 10},
		Network:   v1.NetworkReport{TotalUp: 100, TotalDown: 200},
	}
	second := first
	second.UpdatedAt = base.Add(3 * time.Second)
	second.CPU.Usage = 20
	second.Network.TotalUp = 150
	second.Network.TotalDown = 260

	if _, err := WriteReport(ctx, first); err != nil {
		t.Fatalf("queue first report: %v", err)
	}
	if _, err := WriteReport(ctx, second); err != nil {
		t.Fatalf("queue second report: %v", err)
	}
	points, err := s.Query(ctx, metric.Query{
		MetricName: MetricCPU,
		EntityID:   first.UUID,
		Start:      base.Add(-time.Second),
		End:        base.Add(time.Minute),
		Order:      metric.OrderAsc,
	})
	if err != nil {
		t.Fatalf("query before flush: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("queued reports were written before flush: %#v", points)
	}

	if err := FlushReportBatch(ctx); err != nil {
		t.Fatalf("flush report batch: %v", err)
	}
	assertMetricValues(t, s, MetricCPU, first.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{10, 20})
	assertMetricValues(t, s, MetricTrafficUp, first.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 50})
	assertMetricValues(t, s, MetricTrafficDown, first.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 60})
}

func TestFullReportQueueRejectsNewReportWithoutDroppingData(t *testing.T) {
	ctx := context.Background()
	worker := &reportBatchWorker{
		queue:    make(chan v1.Report, 1),
		requests: make(chan reportBatchRequest, 1),
		done:     make(chan struct{}),
	}
	worker.queue <- v1.Report{UUID: "already-queued"}
	report := v1.Report{
		UUID:      "realtime-node",
		UpdatedAt: time.Now().UTC(),
	}
	if err := worker.enqueue(ctx, report); !errors.Is(err, ErrReportBatchQueueFull) {
		t.Fatalf("queue full error = %v, want ErrReportBatchQueueFull", err)
	}
}

func TestDrainReportQueueUsesCurrentDepthAndHonorsLimit(t *testing.T) {
	empty := make(chan v1.Report, 8_192)
	if got := drainReportQueue(empty, 8_192); got != nil {
		t.Fatalf("empty queue drain = %#v, want nil", got)
	}
	if allocations := testing.AllocsPerRun(1_000, func() {
		_ = drainReportQueue(empty, 8_192)
	}); allocations != 0 {
		t.Fatalf("empty queue drain allocations = %v, want 0", allocations)
	}

	queue := make(chan v1.Report, 8_192)
	for _, uuid := range []string{"a", "b", "c"} {
		queue <- v1.Report{UUID: uuid}
	}
	got := drainReportQueue(queue, 2)
	if len(got) != 2 || cap(got) != 2 || got[0].UUID != "a" || got[1].UUID != "b" {
		t.Fatalf("limited queue drain = %#v (cap %d)", got, cap(got))
	}
	remaining := drainReportQueue(queue, 8_192)
	if len(remaining) != 1 || cap(remaining) != 1 || remaining[0].UUID != "c" {
		t.Fatalf("remaining queue drain = %#v (cap %d)", remaining, cap(remaining))
	}
}

func TestRecordReconstructionUsesMetricSpecificAggregation(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	base := time.Now().UTC().Truncate(time.Minute)
	entityID := "node-aggregation"
	points := []metric.Point{
		{MetricName: MetricCPU, EntityID: entityID, Timestamp: base.Add(time.Second), Value: 10},
		{MetricName: MetricCPU, EntityID: entityID, Timestamp: base.Add(2 * time.Second), Value: 30},
		{MetricName: MetricNetTotalUp, EntityID: entityID, Timestamp: base.Add(time.Second), Value: 100},
		{MetricName: MetricNetTotalUp, EntityID: entityID, Timestamp: base.Add(2 * time.Second), Value: 200},
		{MetricName: MetricTrafficUp, EntityID: entityID, Timestamp: base.Add(time.Second), Value: 10},
		{MetricName: MetricTrafficUp, EntityID: entityID, Timestamp: base.Add(2 * time.Second), Value: 20},
	}
	if err := s.WriteBatch(ctx, points); err != nil {
		t.Fatalf("write points: %v", err)
	}

	records, err := GetRecordsByClientAndTime(ctx, entityID, base, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("reconstruct records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one bucket", records)
	}
	if records[0].Cpu != 20 || records[0].NetTotalUp != 200 || records[0].TrafficUp != 30 {
		t.Fatalf("unexpected aggregation result: %#v", records[0])
	}
}

func TestLoadMetricProjectionKeepsAverageAggregation(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	base := time.Now().UTC().Truncate(time.Hour)
	entityID := "node-load-projection"
	if err := s.WriteBatch(ctx, []metric.Point{
		{MetricName: MetricCPU, EntityID: entityID, Timestamp: base.Add(time.Second), Value: 10},
		{MetricName: MetricCPU, EntityID: entityID, Timestamp: base.Add(2 * time.Second), Value: 90},
		{MetricName: MetricRAM, EntityID: entityID, Timestamp: base.Add(time.Second), Value: 999},
	}); err != nil {
		t.Fatalf("write projected metrics: %v", err)
	}

	records, err := GetRecordsByClientAndTimeForLoadType(ctx, entityID, base, base.Add(48*time.Hour), "cpu")
	if err != nil {
		t.Fatalf("query CPU projection: %v", err)
	}
	if len(records) != 1 || records[0].Cpu != 50 {
		t.Fatalf("CPU projection = %#v, want average 50", records)
	}
	if records[0].Ram != 0 {
		t.Fatalf("CPU projection unexpectedly decoded RAM: %#v", records[0])
	}
}

func TestTrafficCounterDelta(t *testing.T) {
	tests := []struct {
		name     string
		current  int64
		previous int64
		want     int64
	}{
		{name: "previous zero", current: 120, previous: 0, want: 120},
		{name: "monotonic counter", current: 250, previous: 200, want: 50},
		{name: "unchanged counter", current: 100, previous: 100, want: 0},
		{name: "counter reset", current: 15, previous: 250, want: 15},
		{name: "negative current", current: -1, previous: 100, want: 0},
		{name: "negative previous", current: 15, previous: -1, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := TrafficCounterDelta(test.current, test.previous); got != test.want {
				t.Fatalf("TrafficCounterDelta(%d, %d) = %d, want %d", test.current, test.previous, got, test.want)
			}
		})
	}
}

func TestWriteReportNormalizesReceiveTimeToUTC(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	local := time.FixedZone("UTC+8", 8*60*60)
	receiveTime := time.Date(2026, 7, 17, 9, 30, 0, 123456789, local)
	report := v1.Report{
		UUID:      "utc-report",
		UpdatedAt: receiveTime,
		CPU:       v1.CPUReport{Usage: 10},
		Network:   v1.NetworkReport{TotalUp: 1, TotalDown: 2},
	}

	saved, err := WriteReport(ctx, report)
	if err != nil {
		t.Fatalf("write report: %v", err)
	}
	if !saved.UpdatedAt.Equal(receiveTime) || saved.UpdatedAt.Location() != time.UTC {
		t.Fatalf("saved receive time = %s (%s), want UTC", saved.UpdatedAt, saved.UpdatedAt.Location())
	}
	points, err := s.Query(ctx, metric.Query{
		MetricName: MetricCPU,
		EntityID:   report.UUID,
		Start:      receiveTime.Add(-time.Nanosecond),
		End:        receiveTime.Add(time.Nanosecond),
	})
	if err != nil {
		t.Fatalf("query stored point: %v", err)
	}
	if len(points) != 1 || points[0].Timestamp.Location() != time.UTC || points[0].Timestamp.Nanosecond() != 123456789 {
		t.Fatalf("stored points = %#v, want one UTC nanosecond point", points)
	}
}

func assertMetricValues(t *testing.T, s *metric.Store, metricName, entityID string, start, end time.Time, want []float64) {
	t.Helper()
	points, err := s.Query(context.Background(), metric.Query{
		MetricName: metricName,
		EntityID:   entityID,
		Start:      start,
		End:        end,
		Order:      metric.OrderAsc,
	})
	if err != nil {
		t.Fatalf("query %s: %v", metricName, err)
	}
	if len(points) != len(want) {
		t.Fatalf("%s point count = %d, want %d: %#v", metricName, len(points), len(want), points)
	}
	for i := range want {
		if points[i].Value != want[i] {
			t.Fatalf("%s point %d = %v, want %v", metricName, i, points[i].Value, want[i])
		}
	}
}
