package metric

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestPingTaskDistributionsMergesRollupMoments(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store, err := Open(ctx, SQLite(":memory:",
		WithMaxOpenConns(1),
		WithRollupPolicy(RollupPolicy{
			RawRetention: 2 * time.Minute,
			Tiers:        []RollupTier{{Interval: time.Minute, Retention: 24 * time.Hour}},
		}),
	))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertMetric(ctx, Definition{Name: sqliteMergedPingLatencyMetric, Type: TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	points := []Point{
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: now.Add(-10 * time.Minute), Value: 10, Tags: map[string]string{"task_id": "7"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: now.Add(-9 * time.Minute), Value: 110, Tags: map[string]string{"task_id": "7"}},
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}
	got, err := store.PingTaskDistributions(ctx, "node-a", now.Add(-15*time.Minute), now, now)
	if err != nil {
		t.Fatal(err)
	}
	dist, ok := got["7"]
	if !ok || !dist.OK {
		t.Fatalf("distribution = %#v", got)
	}
	if math.Abs(dist.StdDev-50) > 0.01 {
		t.Fatalf("stddev = %v, want 50", dist.StdDev)
	}
	if dist.P99 <= 100 {
		t.Fatalf("p99 = %v, want the high sample rather than the bucket average 60", dist.P99)
	}
}

func TestPingTaskDistributionsKeepsHistoryWhenWindowIsNotTierAligned(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := openPingDistributionStore(t, RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 48 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 14 * 24 * time.Hour},
		},
	})
	writePingSamples(t, store, "node-a", "7", []Point{
		{Timestamp: now.Add(-30 * time.Minute), Value: 10},
		{Timestamp: now.Add(-29 * time.Minute), Value: 110},
		{Timestamp: now.Add(-time.Minute), Value: 60},
	})
	if _, err := store.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}
	aligned := distributionFor(t, store, "node-a", now.Add(-3600*time.Second), now, now)
	for _, start := range []time.Time{
		now.Add(-3601 * time.Second),
		now.Add(-3600*time.Second - time.Millisecond),
		now.Add(-3600*time.Second - time.Nanosecond),
	} {
		got := distributionFor(t, store, "node-a", start, now, now)
		if !sameDistribution(aligned, got) {
			t.Fatalf("start %s distribution = %+v, aligned = %+v", start, got, aligned)
		}
	}
	if aligned.P99 <= 100 || aligned.StdDev < 40 || aligned.StdDev > 41 {
		t.Fatalf("history was dropped or collapsed: %+v", aligned)
	}

	historyOnly := openPingDistributionStore(t, RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers:        []RollupTier{{Interval: time.Minute, Retention: 48 * time.Hour}},
	})
	writePingSamples(t, historyOnly, "node-a", "7", []Point{
		{Timestamp: now.Add(-30 * time.Minute), Value: 10},
		{Timestamp: now.Add(-29 * time.Minute), Value: 110},
	})
	if _, err := historyOnly.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}
	hour := distributionFor(t, historyOnly, "node-a", now.Add(-3600*time.Second), now, now)
	off := distributionFor(t, historyOnly, "node-a", now.Add(-3601*time.Second), now, now)
	if !sameDistribution(hour, off) {
		t.Fatalf("pure history 3601s = %+v, 3600s = %+v", off, hour)
	}
	if math.Abs(hour.StdDev-50) > 0.01 || hour.P99 <= 100 {
		t.Fatalf("pure history distribution = %+v", hour)
	}

	coarse := openPingDistributionStore(t, RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 20 * time.Minute},
			{Interval: 5 * time.Minute, Retention: 24 * time.Hour},
		},
	})
	writePingSamples(t, coarse, "node-a", "7", []Point{
		{Timestamp: now.Add(-40 * time.Minute), Value: 10},
		{Timestamp: now.Add(-35 * time.Minute), Value: 110},
	})
	if _, err := coarse.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}
	coarseHour := distributionFor(t, coarse, "node-a", now.Add(-3600*time.Second), now, now)
	coarseOff := distributionFor(t, coarse, "node-a", now.Add(-3601*time.Second), now, now)
	if !sameDistribution(coarseHour, coarseOff) || math.Abs(coarseHour.StdDev-50) > 0.01 {
		t.Fatalf("coarser tier distribution hour=%+v off=%+v", coarseHour, coarseOff)
	}
}

func TestPingTaskDistributionsWaitsForHeavyReadSlot(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	policy := RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers:        []RollupTier{{Interval: time.Minute, Retention: 48 * time.Hour}},
	}
	rawStore := openPingDistributionStoreWith(t, policy, WithHeavyReadConcurrency(1))
	writePingSamples(t, rawStore, "node-a", "7", []Point{
		{Timestamp: now.Add(-time.Minute), Value: 12},
	})
	history := openPingDistributionStoreWith(t, policy, WithHeavyReadConcurrency(1))
	writePingSamples(t, history, "node-a", "7", []Point{
		{Timestamp: now.Add(-30 * time.Minute), Value: 10},
		{Timestamp: now.Add(-29 * time.Minute), Value: 110},
	})
	if _, err := history.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, driver := range []Driver{DriverSQLite, DriverMySQL, DriverPostgreSQL} {
		t.Run(string(driver)+"/raw", func(t *testing.T) {
			rawStore.cfg.Driver = driver
			assertDistributionWaits(t, rawStore, now.Add(-2*time.Minute), now, now)
		})
		t.Run(string(driver)+"/history", func(t *testing.T) {
			history.cfg.Driver = driver
			assertDistributionWaits(t, history, now.Add(-3600*time.Second), now, now)
		})
	}
}

func assertDistributionWaits(t *testing.T, store *Store, start, end, now time.Time) {
	t.Helper()
	store.heavyReadGate <- struct{}{}
	released := false
	release := func() {
		if released {
			return
		}
		<-store.heavyReadGate
		released = true
	}
	defer release()

	waitCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := store.PingTaskDistributions(waitCtx, "node-a", start, end, now); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("occupied slot err = %v, want deadline exceeded", err)
	}

	cancelCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := store.PingTaskDistributions(cancelCtx, "node-a", start, end, now); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled slot err = %v, want canceled", err)
	}

	release()
	doneCtx, doneCancel := context.WithTimeout(context.Background(), time.Second)
	defer doneCancel()
	got, err := store.PingTaskDistributions(doneCtx, "node-a", start, end, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("distribution empty after the slot was released")
	}
}

func openPingDistributionStore(t *testing.T, policy RollupPolicy) *Store {
	t.Helper()
	return openPingDistributionStoreWith(t, policy)
}

func openPingDistributionStoreWith(t *testing.T, policy RollupPolicy, options ...Option) *Store {
	t.Helper()
	opts := []Option{WithMaxOpenConns(1), WithRollupPolicy(policy)}
	opts = append(opts, options...)
	store, err := Open(context.Background(), SQLite(":memory:", opts...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.UpsertMetric(context.Background(), Definition{Name: sqliteMergedPingLatencyMetric, Type: TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	return store
}

func writePingSamples(t *testing.T, store *Store, entityID, taskID string, samples []Point) {
	t.Helper()
	points := make([]Point, len(samples))
	for index, sample := range samples {
		points[index] = Point{
			MetricName: sqliteMergedPingLatencyMetric,
			EntityID:   entityID,
			Timestamp:  sample.Timestamp,
			Value:      sample.Value,
			Tags:       map[string]string{"task_id": taskID},
		}
	}
	if err := store.WriteBatch(context.Background(), points); err != nil {
		t.Fatal(err)
	}
}

func distributionFor(t *testing.T, store *Store, entityID string, start, end, now time.Time) PingDistribution {
	t.Helper()
	got, err := store.PingTaskDistributions(context.Background(), entityID, start, end, now)
	if err != nil {
		t.Fatal(err)
	}
	dist, ok := got["7"]
	if !ok || !dist.OK {
		t.Fatalf("distribution = %#v", got)
	}
	return dist
}

func sameDistribution(left, right PingDistribution) bool {
	return left.OK && right.OK &&
		math.Abs(left.P50-right.P50) < 0.000001 &&
		math.Abs(left.P99-right.P99) < 0.000001 &&
		math.Abs(left.StdDev-right.StdDev) < 0.000001
}
