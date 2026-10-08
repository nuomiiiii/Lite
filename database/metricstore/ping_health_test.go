package metricstore

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/pkg/metric"
)

func TestPingHealthStatsFromLatencyPoints(t *testing.T) {
	stats := PingHealthStatsFromLatencyPoints([]metric.AggregatePoint{
		{Value: 100, Count: 1},
		{Value: 110, Count: 1},
		{Value: 120, Count: 1},
	})
	if !stats.HasLatency || stats.Successful != 3 || stats.Lost != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if math.Abs(stats.AverageLatencyMS-110) > 0.001 {
		t.Fatalf("avg = %v", stats.AverageLatencyMS)
	}

	mixed := PingHealthStatsFromLatencyPoints([]metric.AggregatePoint{
		{Value: 100, Count: 1},
		{Value: -1, Count: 1},
		{Value: 200, Count: 1},
	})
	if mixed.Total != 3 || mixed.Lost != 1 || mixed.Successful != 2 {
		t.Fatalf("mixed counts = %+v", mixed)
	}
	if math.Abs(mixed.AverageLatencyMS-150) > 0.001 {
		t.Fatalf("mixed avg = %v", mixed.AverageLatencyMS)
	}
	if mixed.MinLatencyMS != 100 || mixed.MaxLatencyMS != 200 {
		t.Fatalf("mixed min/max = %+v", mixed)
	}

	weighted := PingHealthStatsFromLatencyPoints([]metric.AggregatePoint{
		{Value: 100, Count: 2},
		{Value: 200, Count: 1},
	})
	if math.Abs(weighted.AverageLatencyMS-133.33) > 0.001 {
		t.Fatalf("weighted avg = %v", weighted.AverageLatencyMS)
	}

	lost := PingHealthStatsFromLatencyPoints([]metric.AggregatePoint{
		{Value: -1, Count: 4},
	})
	if lost.HasLatency || lost.AverageLatencyMS != 0 || lost.Successful != 0 || lost.Lost != 4 {
		t.Fatalf("all-loss stats = %+v", lost)
	}
}

func TestPingBaselineCandidateExcludesFailures(t *testing.T) {
	candidate := PingBaselineCandidateFromValues([]float64{100, -1, 200, 110})
	if candidate.Successful != 3 {
		t.Fatalf("successful = %d", candidate.Successful)
	}
	if math.Abs(candidate.MedianMS-110) > 0.001 {
		t.Fatalf("median = %v", candidate.MedianMS)
	}
}

func TestAdaptiveBaselineRangeExcludesCurrentWindow(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	start, end := AdaptiveBaselineRange(now, 300, 86400, nil)
	if !end.Equal(now.Add(-5 * time.Minute).Add(-time.Nanosecond)) {
		t.Fatalf("end = %s", end)
	}
	if !start.Equal(end.Add(-24 * time.Hour)) {
		t.Fatalf("start = %s", start)
	}
}

func TestQueryPingHealthStatsAndBaseline(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	store, err := metric.Open(ctx, metric.SQLite(":memory:", metric.WithMaxOpenConns(1)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertMetric(ctx, metric.Definition{Name: MetricPingLatency, Type: metric.TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	points := []metric.Point{
		{MetricName: MetricPingLatency, EntityID: "node-a", Timestamp: now.Add(-90 * time.Second), Value: 100, Tags: map[string]string{"task_id": "7"}},
		{MetricName: MetricPingLatency, EntityID: "node-a", Timestamp: now.Add(-60 * time.Second), Value: -1, Tags: map[string]string{"task_id": "7"}},
		{MetricName: MetricPingLatency, EntityID: "node-a", Timestamp: now.Add(-30 * time.Second), Value: 200, Tags: map[string]string{"task_id": "7"}},
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}

	stats, err := QueryPingHealthStats(ctx, store, "node-a", 7, now.Add(-2*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.Lost != 1 || stats.Successful != 2 {
		t.Fatalf("query counts = %+v", stats)
	}
	if !stats.HasLatency || math.Abs(stats.AverageLatencyMS-150) > 0.5 {
		t.Fatalf("query avg = %+v", stats)
	}
	if stats.MinLatencyMS < 99 || stats.MinLatencyMS > 101 || stats.MaxLatencyMS < 199 || stats.MaxLatencyMS > 201 {
		t.Fatalf("query min/max = %+v", stats)
	}

	historyStart, historyEnd := AdaptiveBaselineRange(now, 30, 120, nil)
	if !historyEnd.Equal(now.Add(-30 * time.Second).Add(-time.Nanosecond)) {
		t.Fatalf("history end = %s", historyEnd)
	}
	candidate, err := QueryPingBaselineCandidate(ctx, store, "node-a", 7, historyStart, historyEnd)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Successful != 1 {
		t.Fatalf("baseline window should exclude the current sample, got %+v", candidate)
	}
	if math.Abs(candidate.MedianMS-100) > 0.5 {
		t.Fatalf("baseline median = %+v", candidate)
	}
}

func TestAdaptiveBaselineRangeClampsToStartedAt(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	started := now.Add(-2 * time.Hour)
	start, end := AdaptiveBaselineRange(now, 300, 86400, &started)
	if !end.Equal(now.Add(-5 * time.Minute).Add(-time.Nanosecond)) {
		t.Fatalf("end = %s", end)
	}
	if !start.Equal(started.UTC()) {
		t.Fatalf("start = %s", start)
	}

	justChanged := now
	emptyStart, emptyEnd := AdaptiveBaselineRange(now, 300, 86400, &justChanged)
	if AdaptiveBaselineRangeValid(emptyStart, emptyEnd) {
		t.Fatalf("identity-reset window must be empty, start=%s end=%s", emptyStart, emptyEnd)
	}
}

func TestQueryPingBaselineCandidateIgnoresSamplesBeforeStartedAt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	store, err := metric.Open(ctx, metric.SQLite(":memory:", metric.WithMaxOpenConns(1)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertMetric(ctx, metric.Definition{Name: MetricPingLatency, Type: metric.TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	points := []metric.Point{
		{MetricName: MetricPingLatency, EntityID: "node-a", Timestamp: now.Add(-3 * time.Hour), Value: 150, Tags: map[string]string{"task_id": "7"}},
		{MetricName: MetricPingLatency, EntityID: "node-a", Timestamp: now.Add(-20 * time.Minute), Value: 50, Tags: map[string]string{"task_id": "7"}},
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	started := now.Add(-30 * time.Minute)
	historyStart, historyEnd := AdaptiveBaselineRange(now, 300, 86400, &started)
	candidate, err := QueryPingBaselineCandidate(ctx, store, "node-a", 7, historyStart, historyEnd)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Successful != 1 {
		t.Fatalf("post-change samples = %+v", candidate)
	}
	if math.Abs(candidate.MedianMS-50) > 0.5 {
		t.Fatalf("post-change median = %+v", candidate)
	}
}

func TestQueryPingBaselineCandidateOverRollupDay(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 1, 50, 0, 0, time.UTC)
	store, err := metric.Open(ctx, metric.SQLite(":memory:",
		metric.WithMaxOpenConns(1),
		metric.WithRollupPolicy(defaultRollupPolicy()),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertMetric(ctx, metric.Definition{Name: MetricPingLatency, Type: metric.TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	points := make([]metric.Point, 0, 24*60)
	for i := 1; i <= 24*60; i++ {
		points = append(points, metric.Point{
			MetricName: MetricPingLatency,
			EntityID:   "node-a",
			Timestamp:  now.Add(-time.Duration(i) * time.Minute),
			Value:      1,
			Tags:       map[string]string{"task_id": "7"},
		})
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}
	start, end := AdaptiveBaselineRange(now, 300, 86400, nil)
	candidate, err := QueryPingBaselineCandidate(ctx, store, "node-a", 7, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Successful < 30 {
		t.Fatalf("successful = %d", candidate.Successful)
	}
	if candidate.MedianMS <= 0 {
		t.Fatalf("median = %v, want about 1ms", candidate.MedianMS)
	}
}

func TestQueryPingBaselineCandidateUsesAverageWhenMedianIsZero(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 1, 50, 0, 0, time.UTC)
	store := openPingBaselineStore(t)
	points := make([]metric.Point, 0, 40)
	for i := 0; i < 40; i++ {
		value := 0.0
		if i >= 30 {
			value = 2
		}
		points = append(points, metric.Point{
			MetricName: MetricPingLatency,
			EntityID:   "node-a",
			Timestamp:  now.Add(-6*time.Minute - time.Duration(i)*time.Second),
			Value:      value,
			Tags:       map[string]string{"task_id": "7"},
		})
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	start, end := AdaptiveBaselineRange(now, 300, 86400, nil)
	candidate, err := QueryPingBaselineCandidate(ctx, store, "node-a", 7, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Successful != 40 {
		t.Fatalf("successful = %d", candidate.Successful)
	}
	if math.Abs(candidate.MedianMS-0.5) > 0.05 {
		t.Fatalf("median = %v, want about 0.5ms", candidate.MedianMS)
	}
}

func TestQueryPingBaselineCandidateStaysUnsetWhenLatencyIsZero(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 1, 50, 0, 0, time.UTC)
	store := openPingBaselineStore(t)
	points := make([]metric.Point, 0, 40)
	for i := 0; i < 40; i++ {
		points = append(points, metric.Point{
			MetricName: MetricPingLatency,
			EntityID:   "node-a",
			Timestamp:  now.Add(-6*time.Minute - time.Duration(i)*time.Second),
			Value:      0,
			Tags:       map[string]string{"task_id": "7"},
		})
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	start, end := AdaptiveBaselineRange(now, 300, 86400, nil)
	candidate, err := QueryPingBaselineCandidate(ctx, store, "node-a", 7, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Successful != 40 || candidate.MedianMS != 0 {
		t.Fatalf("candidate = %+v, want 40 successes and no baseline", candidate)
	}
}

func openPingBaselineStore(t *testing.T) *metric.Store {
	t.Helper()
	store, err := metric.Open(context.Background(), metric.SQLite(":memory:", metric.WithMaxOpenConns(1)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.UpsertMetric(context.Background(), metric.Definition{Name: MetricPingLatency, Type: metric.TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	return store
}
