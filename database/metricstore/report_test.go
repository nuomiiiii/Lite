package metricstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/metric"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func TestDashboardTrafficBatchMatchesPerClientSeries(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	start := time.Now().UTC().Truncate(time.Hour)
	end := start.Add(time.Hour)
	clients := []string{"node-a", "node-b"}
	points := make([]metric.Point, 0, len(clients)*12)
	for index, client := range clients {
		base := float64((index + 1) * 1000)
		points = append(points,
			metric.Point{MetricName: MetricNetTotalUp, EntityID: client, Timestamp: start.Add(-5 * time.Second), Value: base},
			metric.Point{MetricName: MetricNetTotalDown, EntityID: client, Timestamp: start.Add(-5 * time.Second), Value: base * 2},
		)
		for sample := 1; sample <= 2; sample++ {
			ts := start.Add(time.Duration(sample*10) * time.Second)
			points = append(points,
				metric.Point{MetricName: MetricNetTotalUp, EntityID: client, Timestamp: ts, Value: base + float64(sample*10)},
				metric.Point{MetricName: MetricNetTotalDown, EntityID: client, Timestamp: ts, Value: base*2 + float64(sample*20)},
				metric.Point{MetricName: MetricTrafficUp, EntityID: client, Timestamp: ts, Value: 10},
				metric.Point{MetricName: MetricTrafficDown, EntityID: client, Timestamp: ts, Value: 20},
			)
		}
	}
	if err := s.WriteBatch(ctx, points); err != nil {
		t.Fatalf("write dashboard traffic points: %v", err)
	}

	batch, baselines, err := GetTrafficRecordsByClientsAndTime(ctx, clients, start, end)
	if err != nil {
		t.Fatalf("query dashboard traffic batch: %v", err)
	}
	var legacy []models.Record
	for _, client := range clients {
		records, err := GetTrafficRecordsByClientAndTime(ctx, client, start, end)
		if err != nil {
			t.Fatalf("query legacy traffic for %s: %v", client, err)
		}
		legacy = append(legacy, records...)
		baseline := baselines[client]
		if baseline.NetTotalUp == 0 || baseline.NetTotalDown == 0 {
			t.Fatalf("missing baseline for %s: %#v", client, baseline)
		}
	}
	sortRecords(legacy)
	converted := make([]models.Record, 0, len(batch))
	for _, record := range batch {
		converted = append(converted, models.Record{
			Client: record.Client, Time: record.Time,
			NetTotalUp: record.NetTotalUp, NetTotalDown: record.NetTotalDown,
			TrafficUp: record.TrafficUp, TrafficDown: record.TrafficDown,
			TrafficUpSet: record.TrafficUpSet, TrafficDownSet: record.TrafficDownSet,
		})
	}
	if !reflect.DeepEqual(converted, legacy) {
		t.Fatalf("batch traffic differs from per-client result\nbatch=%#v\nlegacy=%#v", batch, legacy)
	}
}

func TestDashboardTrafficBatchMatchesPerClientAcrossDiscontinuities(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	start := time.Now().UTC().Truncate(time.Hour)
	end := start.Add(time.Hour)
	clients := []string{"reset", "interface-change", "missing"}
	points := []metric.Point{
		{MetricName: MetricNetTotalUp, EntityID: "reset", Timestamp: start.Add(-5 * time.Second), Value: 100},
		{MetricName: MetricNetTotalDown, EntityID: "reset", Timestamp: start.Add(-5 * time.Second), Value: 200},
		{MetricName: MetricNetTotalUp, EntityID: "reset", Timestamp: start.Add(10 * time.Second), Value: 150},
		{MetricName: MetricNetTotalDown, EntityID: "reset", Timestamp: start.Add(10 * time.Second), Value: 260},
		{MetricName: MetricTrafficUp, EntityID: "reset", Timestamp: start.Add(10 * time.Second), Value: 50},
		{MetricName: MetricTrafficDown, EntityID: "reset", Timestamp: start.Add(10 * time.Second), Value: 60},
		{MetricName: MetricNetTotalUp, EntityID: "reset", Timestamp: start.Add(20 * time.Second), Value: 20},
		{MetricName: MetricNetTotalDown, EntityID: "reset", Timestamp: start.Add(20 * time.Second), Value: 30},
		{MetricName: MetricTrafficUp, EntityID: "reset", Timestamp: start.Add(20 * time.Second), Value: 0},
		{MetricName: MetricTrafficDown, EntityID: "reset", Timestamp: start.Add(20 * time.Second), Value: 0},
		{MetricName: MetricNetTotalUp, EntityID: "reset", Timestamp: start.Add(30 * time.Second), Value: 35},
		{MetricName: MetricNetTotalDown, EntityID: "reset", Timestamp: start.Add(30 * time.Second), Value: 50},
		{MetricName: MetricTrafficUp, EntityID: "reset", Timestamp: start.Add(30 * time.Second), Value: 15},
		{MetricName: MetricTrafficDown, EntityID: "reset", Timestamp: start.Add(30 * time.Second), Value: 20},
		{MetricName: MetricNetTotalUp, EntityID: "interface-change", Timestamp: start.Add(-5 * time.Second), Value: 1_000},
		{MetricName: MetricNetTotalDown, EntityID: "interface-change", Timestamp: start.Add(-5 * time.Second), Value: 2_000},
		{MetricName: MetricNetTotalUp, EntityID: "interface-change", Timestamp: start.Add(10 * time.Second), Value: 1_010},
		{MetricName: MetricNetTotalDown, EntityID: "interface-change", Timestamp: start.Add(10 * time.Second), Value: 2_020},
		{MetricName: MetricTrafficUp, EntityID: "interface-change", Timestamp: start.Add(10 * time.Second), Value: 10},
		{MetricName: MetricTrafficDown, EntityID: "interface-change", Timestamp: start.Add(10 * time.Second), Value: 20},
		{MetricName: MetricNetTotalUp, EntityID: "interface-change", Timestamp: start.Add(20 * time.Second), Value: 9_000_000},
		{MetricName: MetricNetTotalDown, EntityID: "interface-change", Timestamp: start.Add(20 * time.Second), Value: 8_000_000},
		{MetricName: MetricTrafficUp, EntityID: "interface-change", Timestamp: start.Add(20 * time.Second), Value: 0},
		{MetricName: MetricTrafficDown, EntityID: "interface-change", Timestamp: start.Add(20 * time.Second), Value: 0},
	}
	if err := s.WriteBatch(ctx, points); err != nil {
		t.Fatalf("write discontinuity fixtures: %v", err)
	}

	batch, baselines, err := GetTrafficRecordsByClientsAndTime(ctx, clients, start, end)
	if err != nil {
		t.Fatalf("query dashboard traffic batch: %v", err)
	}
	var legacy []models.Record
	for _, client := range clients {
		records, err := GetTrafficRecordsByClientAndTime(ctx, client, start, end)
		if err != nil {
			t.Fatalf("query legacy traffic for %s: %v", client, err)
		}
		legacy = append(legacy, records...)
	}
	sortRecords(legacy)
	converted := make([]models.Record, 0, len(batch))
	for _, record := range batch {
		converted = append(converted, models.Record{
			Client: record.Client, Time: record.Time,
			NetTotalUp: record.NetTotalUp, NetTotalDown: record.NetTotalDown,
			TrafficUp: record.TrafficUp, TrafficDown: record.TrafficDown,
			TrafficUpSet: record.TrafficUpSet, TrafficDownSet: record.TrafficDownSet,
		})
	}
	if !reflect.DeepEqual(converted, legacy) {
		t.Fatalf("batch traffic differs across counter discontinuities\nbatch=%#v\nlegacy=%#v", batch, legacy)
	}
	if baselines["reset"].NetTotalUp != 100 || baselines["interface-change"].NetTotalDown != 2_000 {
		t.Fatalf("unexpected batch baselines: %#v", baselines)
	}
	if _, ok := baselines["missing"]; ok {
		t.Fatalf("missing node unexpectedly received a baseline: %#v", baselines["missing"])
	}
}

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

	report := v2.Report{
		UUID:        "node-a",
		UpdatedAt:   base,
		CPU:         v2.CPUReport{Usage: 12.5},
		Ram:         v2.RamReport{Used: 100, Total: 1000},
		Swap:        v2.RamReport{Used: 20, Total: 200},
		Load:        v2.LoadReport{Load1: 0.5},
		Disk:        v2.DiskReport{Used: 300, Total: 3000},
		Network:     v2.NetworkReport{Up: 3, Down: 4, TotalUp: 100, TotalDown: 200},
		Process:     7,
		Connections: v2.ConnectionsReport{TCP: 8, UDP: 9},
		GPU: &v2.GPUDetailReport{
			AverageUsage: 25,
			DetailedInfo: []v2.GPUDeviceInfo{{
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

	assertMetricValues(t, s, MetricTrafficUp, report.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 50, 0})
	assertMetricValues(t, s, MetricTrafficDown, report.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 60, 0})
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

func TestWriteReportRebasesTrafficAfterAgentRestart(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	base := time.Now().UTC().Truncate(time.Minute).Add(5 * time.Second)
	report := v2.Report{
		UUID:      "restarted-node",
		UpdatedAt: base,
		Uptime:    1000,
		Network:   v2.NetworkReport{TotalUp: 100, TotalDown: 200},
	}
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write first report: %v", err)
	}

	report.UpdatedAt = base.Add(3 * time.Second)
	report.Uptime = 1003
	report.Network.TotalUp = 150
	report.Network.TotalDown = 260
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write continuous report: %v", err)
	}

	report.UpdatedAt = base.Add(6 * time.Second)
	report.Uptime = 1
	report.Network.TotalUp = 155
	report.Network.TotalDown = 265
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write report after agent restart: %v", err)
	}

	report.UpdatedAt = base.Add(9 * time.Second)
	report.Uptime = 4
	report.Network.TotalUp = 180
	report.Network.TotalDown = 300
	if _, err := WriteReport(ctx, report); err != nil {
		t.Fatalf("write report after new baseline: %v", err)
	}

	assertMetricValues(t, s, MetricTrafficUp, report.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 50, 0, 25})
	assertMetricValues(t, s, MetricTrafficDown, report.UUID, base.Add(-time.Second), base.Add(time.Minute), []float64{0, 60, 0, 35})
}

func TestWriteReportSkipsMetricsWithoutAgentData(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	timestamp := time.Now().UTC()
	if _, err := WriteReport(ctx, v2.Report{
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
	first := v2.Report{
		UUID:      "batched-node",
		UpdatedAt: base,
		CPU:       v2.CPUReport{Usage: 10},
		Network:   v2.NetworkReport{TotalUp: 100, TotalDown: 200},
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
		queue:    make(chan v2.Report, 1),
		requests: make(chan reportBatchRequest, 1),
		done:     make(chan struct{}),
	}
	worker.queue <- v2.Report{UUID: "already-queued"}
	report := v2.Report{
		UUID:      "realtime-node",
		UpdatedAt: time.Now().UTC(),
	}
	if err := worker.enqueue(ctx, report); !errors.Is(err, ErrReportBatchQueueFull) {
		t.Fatalf("queue full error = %v, want ErrReportBatchQueueFull", err)
	}
}

func TestDrainReportQueueUsesCurrentDepthAndHonorsLimit(t *testing.T) {
	empty := make(chan v2.Report, 8_192)
	if got := drainReportQueue(empty, 8_192); got != nil {
		t.Fatalf("empty queue drain = %#v, want nil", got)
	}
	if allocations := testing.AllocsPerRun(1_000, func() {
		_ = drainReportQueue(empty, 8_192)
	}); allocations != 0 {
		t.Fatalf("empty queue drain allocations = %v, want 0", allocations)
	}

	queue := make(chan v2.Report, 8_192)
	for _, uuid := range []string{"a", "b", "c"} {
		queue <- v2.Report{UUID: uuid}
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

func TestRecordPerEntityMaxPointsUsesBoundedOversampling(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name       string
		maxPoints  int
		entities   int
		wantPoints int
	}{
		{name: "five nodes preserve full resolution", maxPoints: 4000, entities: 5, wantPoints: 500},
		{name: "eighty nodes avoid forty thousand temporary rows", maxPoints: 4000, entities: 80, wantPoints: 100},
		{name: "very large fleets keep a useful floor", maxPoints: 4000, entities: 800, wantPoints: 16},
		{name: "unlimited keeps compatibility limit", maxPoints: -1, entities: 80, wantPoints: 500},
		{name: "largest budget does not overflow", maxPoints: maxInt, entities: 1, wantPoints: 500},
		{name: "largest fleet count does not overflow", maxPoints: maxInt, entities: maxInt, wantPoints: 16},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			if got := recordPerEntityMaxPoints(item.maxPoints, item.entities); got != item.wantPoints {
				t.Fatalf("recordPerEntityMaxPoints(%d, %d) = %d, want %d", item.maxPoints, item.entities, got, item.wantPoints)
			}
		})
	}
}

func TestRecordClientMaxPointsPreservesLegacyCap(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{input: -1, want: 500},
		{input: 100, want: 100},
		{input: 500, want: 500},
		{input: 4000, want: 500},
	}
	for _, item := range tests {
		if got := recordClientMaxPoints(item.input); got != item.want {
			t.Fatalf("recordClientMaxPoints(%d) = %d, want %d", item.input, got, item.want)
		}
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
		{name: "counter reset", current: 15, previous: 250, want: 0},
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

func TestReportTrafficCounterDeltaRejectsUnexplainedPositiveJump(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)

	if got := ReportTrafficCounterDelta(2*gib, gib, 0, 3*time.Second); got != 0 {
		t.Fatalf("unexplained positive jump = %d, want 0", got)
	}
	if got := ReportTrafficCounterDelta(2*gib, gib, 300*1024*1024, time.Second); got != gib {
		t.Fatalf("rate-supported positive jump = %d, want %d", got, gib)
	}
	if got := ReportTrafficCounterDelta(gib/2, gib, 300*1024*1024, time.Second); got != 0 {
		t.Fatalf("counter decrease = %d, want 0", got)
	}
}

func TestWriteReportNormalizesReceiveTimeToUTC(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	local := time.FixedZone("UTC+8", 8*60*60)
	receiveTime := time.Date(2026, 7, 17, 9, 30, 0, 123456789, local)
	report := v2.Report{
		UUID:      "utc-report",
		UpdatedAt: receiveTime,
		CPU:       v2.CPUReport{Usage: 10},
		Network:   v2.NetworkReport{TotalUp: 1, TotalDown: 2},
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
