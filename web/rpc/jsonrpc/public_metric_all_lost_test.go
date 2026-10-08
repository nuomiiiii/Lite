package jsonrpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/metric"
)

// A bucket made only of failed probes (value -1) is aggregated by the store
// over zero valid samples, which yields Value 0 with Count equal to the number
// of failed probes. This documents how a 0 reaches the ping stats.
func TestPingLatencyBucketOfOnlyFailuresAggregatesToZero(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	points := []metric.Point{
		{MetricName: "ping.latency_ms", EntityID: "node-a", Timestamp: base, Value: -1},
		{MetricName: "ping.latency_ms", EntityID: "node-a", Timestamp: base.Add(time.Second), Value: -1},
	}
	got, err := metric.AggregatePoints(points, metric.AggregateQuery{
		Query:       metric.Query{MetricName: "ping.latency_ms", EntityID: "node-a", Start: base, End: base.Add(time.Hour)},
		Aggregation: metric.AggAvg,
		Interval:    time.Minute,
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(got) != 1 || got[0].Value != 0 || got[0].Count != 2 {
		t.Fatalf("expected one zero-valued bucket with count 2, got %#v", got)
	}
}

func TestPublicPingStatsFromAggregateGroupsAllLostOmitsLatency(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	zero := map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 0}}}
	taskMap := map[string]models.PingTask{
		"1": {Id: 1, Name: "Tokyo ICMP", Clients: models.StringArray{"node-a"}, Type: "icmp", Interval: 60},
	}
	groups := publicPingMetricAggregateGroups{
		Avg: zero, Min: zero, Max: zero, Last: zero, P50: zero, P99: zero, StdDev: zero,
		Loss:          map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 1}}},
		LossAvailable: true,
	}
	stats := publicPingStatsFromAggregateGroups("node-a", groups, taskMap, nil)
	if len(stats) != 1 {
		t.Fatalf("expected one stat, got %#v", stats)
	}
	got := stats[0]
	if got.Valid != 0 || got.Loss != 100 {
		t.Fatalf("expected valid=0 loss=100, got %#v", got)
	}
	if got.Avg != nil || got.Latest != nil || got.P50 != nil || got.P99 != nil ||
		got.Min != nil || got.Max != nil || got.StdDev != nil {
		t.Fatalf("latency fields must be omitted when every probe failed: %#v", got)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"avg"`, `"latest"`, `"p50"`, `"p99"`, `"min"`, `"max"`, `"std_dev"`} {
		if strings.Contains(string(payload), key) {
			t.Fatalf("%s must be omitted from %s", key, payload)
		}
	}
}

func TestPublicPingStatsFromAggregateGroupsKeepsZeroLatencyWithValidSamples(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	zero := map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 0}}}
	taskMap := map[string]models.PingTask{
		"1": {Id: 1, Name: "LAN", Clients: models.StringArray{"node-a"}, Type: "icmp", Interval: 60},
	}
	groups := publicPingMetricAggregateGroups{
		Avg: zero, Min: zero, Max: zero, Last: zero, P50: zero, P99: zero, StdDev: zero,
		Loss:          map[string][]metric.AggregatePoint{"1": {{Bucket: base, Count: 4, Value: 0}}},
		LossAvailable: true,
		Window:        map[string]metric.PingDistribution{"1": {OK: true}},
	}
	stats := publicPingStatsFromAggregateGroups("node-a", groups, taskMap, nil)
	if len(stats) != 1 {
		t.Fatalf("expected one stat, got %#v", stats)
	}
	got := stats[0]
	if got.Valid != 4 || got.Loss != 0 {
		t.Fatalf("unexpected totals: %#v", got)
	}
	for name, value := range map[string]*float64{
		"avg": got.Avg, "latest": got.Latest, "p50": got.P50, "p99": got.P99,
		"min": got.Min, "max": got.Max, "stddev": got.StdDev,
	} {
		if value == nil || *value != 0 {
			t.Fatalf("%s: sub-millisecond latency 0 must be preserved, got %v", name, value)
		}
	}
}

// With partial loss the store reports a bucket of only failures as Value 0 with
// Count equal to the number of failed probes, and a half-failed bucket averages
// only its valid probes but keeps Count at the total. Latency stats must be
// weighted by valid samples, otherwise those zeros drag avg/min/p50 down.
func TestPublicPingStatsFromAggregateGroupsPartialLossIgnoresFailedSamples(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	bucket := func(i int) time.Time { return base.Add(time.Duration(i) * time.Minute) }
	series := func(v0, v1, v2, v3 float64) map[string][]metric.AggregatePoint {
		return map[string][]metric.AggregatePoint{"1": {
			{Bucket: bucket(0), Count: 1, Value: v0},
			{Bucket: bucket(1), Count: 1, Value: v1}, // every probe failed -> stored as 0
			{Bucket: bucket(2), Count: 1, Value: v2},
			{Bucket: bucket(3), Count: 4, Value: v3}, // 2 valid + 2 failed
		}}
	}
	loss := map[string][]metric.AggregatePoint{"1": {
		{Bucket: bucket(0), Count: 1, Value: 0},
		{Bucket: bucket(1), Count: 1, Value: 1},
		{Bucket: bucket(2), Count: 1, Value: 0},
		{Bucket: bucket(3), Count: 4, Value: 0.5},
	}}
	taskMap := map[string]models.PingTask{
		"1": {Id: 1, Name: "Seattle ICMP", Clients: models.StringArray{"node-a"}, Type: "icmp", Interval: 60},
	}
	groups := publicPingMetricAggregateGroups{
		Avg:           series(200, 0, 220, 100),
		Min:           series(200, 0, 220, 80),
		Max:           series(200, 0, 220, 120),
		Last:          series(200, -1, 220, 90),
		P50:           series(200, 0, 220, 100),
		P99:           series(200, 0, 220, 120),
		StdDev:        series(0, 0, 0, 20),
		Loss:          loss,
		LossAvailable: true,
	}
	stats := publicPingStatsFromAggregateGroups("node-a", groups, taskMap, nil)
	if len(stats) != 1 {
		t.Fatalf("expected one stat, got %#v", stats)
	}
	got := stats[0]
	if got.Total != 7 || got.Valid != 4 {
		t.Fatalf("expected total=7 valid=4, got total=%d valid=%d", got.Total, got.Valid)
	}
	want := map[string]float64{
		"avg": 155, // (200*1 + 220*1 + 100*2) / 4, not (200+0+220+400)/7
		"min": 80,  // the failed bucket's 0 must not become the minimum
		"max": 220,
	}
	have := map[string]*float64{"avg": got.Avg, "min": got.Min, "max": got.Max}
	for name, w := range want {
		g := have[name]
		if g == nil {
			t.Errorf("%s is nil, want %v", name, w)
		} else if *g < w-0.001 || *g > w+0.001 {
			t.Errorf("%s = %v, want %v", name, *g, w)
		}
	}
	if got.P50 != nil || got.P99 != nil || got.StdDev != nil {
		t.Fatalf("bucket percentiles must not stand in for the window distribution: %#v", got)
	}
	if got.Latest == nil {
		t.Errorf("latest is nil, want 90 (the newest successful bucket)")
	} else if *got.Latest != 90 {
		t.Errorf("latest = %v, want 90 (the newest successful bucket)", *got.Latest)
	}
}

// pingStatsFromRawProbes runs raw probe values through the real SQLite store
// aggregation (PingSeriesSummary) and the public stats builder, so the bucket
// alignment between the latency and loss series is exercised for real.
// probes maps a minute offset to the probe values sent during that minute.
func pingStatsFromRawProbes(t *testing.T, probes map[int][]float64, minutes int) publicPingMetricTaskStats {
	t.Helper()
	ctx := context.Background()
	store, err := metric.Open(ctx, metric.SQLite("file:public-ping-stats-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	for _, name := range []string{metricstore.MetricPingLatency, metricstore.MetricPingLoss} {
		if err := store.CreateMetric(ctx, metric.Definition{Name: name, Type: metric.TypeGauge, RetentionDays: 7}); err != nil {
			t.Fatalf("create metric %s: %v", name, err)
		}
	}
	base := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	var points []metric.Point
	for minute, values := range probes {
		for i, value := range values {
			points = append(points, metric.Point{
				MetricName: metricstore.MetricPingLatency,
				EntityID:   "node-a",
				Timestamp:  base.Add(time.Duration(minute)*time.Minute + time.Duration(i)*15*time.Second),
				Value:      value,
				Tags:       map[string]string{"task_id": "1"},
			})
		}
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatalf("write points: %v", err)
	}
	end := base.Add(time.Duration(minutes) * time.Minute)
	summary, err := store.PingSeriesSummary(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: metricstore.MetricPingLatency,
			EntityID:   "node-a",
			Start:      base,
			End:        end,
			Order:      metric.OrderAsc,
		},
		Interval:       time.Minute,
		PreserveSeries: true,
	}, end)
	if err != nil {
		t.Fatalf("ping summary: %v", err)
	}
	taskMap := map[string]models.PingTask{
		"1": {Id: 1, Name: "Seattle ICMP", Clients: models.StringArray{"node-a"}, Type: "icmp", Interval: 60},
	}
	groups := publicPingMetricGroupsFromSummary(summary)
	window, err := store.PingTaskDistributions(ctx, "node-a", base, end, end)
	if err != nil {
		t.Fatalf("window distribution: %v", err)
	}
	groups.Window = window
	stats := publicPingStatsFromAggregateGroups("node-a", groups, taskMap, nil)
	if len(stats) != 1 {
		t.Fatalf("expected one stat, got %#v", stats)
	}
	return stats[0]
}

func TestPublicPingStatsFromRealAggregationIgnoresFailedProbes(t *testing.T) {
	// minute 0: 200 ms; minute 1: every probe failed; minute 2: 220 ms;
	// minute 3: four probes, two failed and two at 100 ms.
	got := pingStatsFromRawProbes(t, map[int][]float64{
		0: {200},
		1: {-1},
		2: {220},
		3: {-1, 100, -1, 100},
	}, 4)
	if got.Total != 7 || got.Valid != 4 {
		t.Fatalf("expected total=7 valid=4, got total=%d valid=%d", got.Total, got.Valid)
	}
	if got.Loss < 42.8 || got.Loss > 42.9 {
		t.Errorf("loss = %v, want ~42.86", got.Loss)
	}
	want := map[string]float64{
		"avg": 155, // (200 + 220 + 100*2) / 4 valid probes, not / 7 total
		"min": 100, // the failed minute's 0 must not become the minimum
		"max": 220,
	}
	have := map[string]*float64{"avg": got.Avg, "min": got.Min, "max": got.Max}
	for name, w := range want {
		g := have[name]
		if g == nil {
			t.Errorf("%s is nil, want %v", name, w)
		} else if *g < w-0.001 || *g > w+0.001 {
			t.Errorf("%s = %v, want %v", name, *g, w)
		}
	}
	if got.Latest == nil || *got.Latest != 100 {
		t.Errorf("latest = %v, want 100 (the newest minute with a successful probe)", got.Latest)
	}
	if got.P50 == nil || *got.P50 != 150 {
		t.Errorf("p50 = %v, want 150", got.P50)
	}
	if got.P99 == nil || *got.P99 != 219.4 {
		t.Errorf("p99 = %v, want 219.4", got.P99)
	}
	if got.StdDev == nil || *got.StdDev < 55.44 || *got.StdDev > 55.46 {
		t.Errorf("stddev = %v, want 55.45", got.StdDev)
	}
}

func TestPublicPingStatsWindowPercentileIsNotBucketAverage(t *testing.T) {
	got := pingStatsFromRawProbes(t, map[int][]float64{
		0: {10},
		1: {110},
	}, 2)
	if got.Avg == nil || *got.Avg != 60 {
		t.Fatalf("avg = %v, want 60", got.Avg)
	}
	if got.P99 == nil || *got.P99 != 109 {
		t.Fatalf("p99 = %v, want 109", got.P99)
	}
	if got.StdDev == nil || *got.StdDev != 50 {
		t.Fatalf("stddev = %v, want 50", got.StdDev)
	}
}

func TestPublicPingStatsFromRealAggregationAllLost(t *testing.T) {
	got := pingStatsFromRawProbes(t, map[int][]float64{
		0: {-1},
		1: {-1},
		2: {-1},
	}, 3)
	if got.Total != 3 || got.Valid != 0 || got.Loss != 100 {
		t.Fatalf("expected total=3 valid=0 loss=100, got %#v", got)
	}
	if got.Avg != nil || got.Latest != nil || got.P50 != nil || got.P99 != nil ||
		got.Min != nil || got.Max != nil || got.StdDev != nil {
		t.Fatalf("latency fields must be omitted when every probe failed: %#v", got)
	}
}
