package metric

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestDashboardSeriesMatchesTaggedMergedPingAverage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, SQLite("file:dashboard-series-ping?mode=memory&cache=shared", WithMaxOpenConns(1)))
	if err != nil {
		t.Fatalf("open dashboard series store: %v", err)
	}
	defer store.Close()
	if err := store.CreateMetric(ctx, Definition{Name: sqliteMergedPingLatencyMetric, Type: TypeGauge, RetentionDays: 7}); err != nil {
		t.Fatalf("create ping metric: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Hour)
	points := []Point{
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: base.Add(time.Minute), Value: 20, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: base.Add(2 * time.Minute), Value: -1, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: base.Add(3 * time.Minute), Value: 40, Tags: map[string]string{"task_id": "one"}},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-b", Timestamp: base.Add(time.Minute), Value: 80, Tags: map[string]string{"task_id": "two"}},
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatalf("write ping points: %v", err)
	}
	query := AggregateQuery{
		Query:       Query{MetricName: sqliteMergedPingLatencyMetric, Start: base, End: base.Add(time.Hour), Order: OrderAsc},
		Aggregation: AggAvg, Interval: time.Hour, PreserveSeries: true,
	}
	want, err := store.Series(ctx, query, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("query compatibility series: %v", err)
	}
	got, err := store.DashboardSeries(ctx, query, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("query dashboard series: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dashboard series changed tagged ping semantics\ngot=%#v\nwant=%#v", got, want)
	}

	lossQuery := query
	lossQuery.MetricName = sqliteVirtualPingLossMetric
	wantLoss, err := store.Series(ctx, lossQuery, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("query compatibility loss series: %v", err)
	}
	gotLoss, err := store.DashboardSeries(ctx, lossQuery, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("query dashboard loss series: %v", err)
	}
	if !reflect.DeepEqual(gotLoss, wantLoss) || len(gotLoss) != 2 || gotLoss[0].MetricName != sqliteVirtualPingLossMetric {
		t.Fatalf("dashboard series changed virtual ping loss semantics\ngot=%#v\nwant=%#v", gotLoss, wantLoss)
	}
}

func TestDashboardSeriesLetsHotValueOverrideSealedBlock(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, SQLite("file:dashboard-series-overlap?mode=memory&cache=shared", WithMaxOpenConns(1)))
	if err != nil {
		t.Fatalf("open dashboard series store: %v", err)
	}
	defer store.Close()
	if err := store.CreateMetric(ctx, Definition{Name: "counter", Type: TypeCounter, RetentionDays: 7}); err != nil {
		t.Fatalf("create counter metric: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Minute)
	if err := store.WriteBatch(ctx, []Point{
		{MetricName: "counter", EntityID: "node-a", Timestamp: base, Value: 10},
		{MetricName: "counter", EntityID: "node-a", Timestamp: base.Add(10 * time.Second), Value: 20},
	}); err != nil {
		t.Fatalf("write initial points: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin seal transaction: %v", err)
	}
	sealed, err := store.sealSQLiteV4PointsTx(ctx, tx, "counter", math.MaxInt64, 2, 0)
	if err != nil || sealed != 2 {
		_ = tx.Rollback()
		t.Fatalf("seal points: count=%d err=%v", sealed, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit sealed points: %v", err)
	}
	if err := store.Write(ctx, Point{MetricName: "counter", EntityID: "node-a", Timestamp: base.Add(10 * time.Second), Value: 25}); err != nil {
		t.Fatalf("write hot override: %v", err)
	}
	query := AggregateQuery{
		Query:       Query{MetricName: "counter", Start: base, End: base.Add(time.Minute), Order: OrderAsc},
		Aggregation: AggLast, Interval: time.Minute, PreserveSeries: true, OmitTags: true,
	}
	want, err := store.Series(ctx, query, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("query compatibility overlap: %v", err)
	}
	got, err := store.DashboardSeries(ctx, query, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("query dashboard overlap: %v", err)
	}
	if len(got) != 1 || len(want) != 1 ||
		got[0].MetricName != want[0].MetricName || got[0].EntityID != want[0].EntityID ||
		!got[0].Bucket.Equal(want[0].Bucket) || got[0].Value != want[0].Value || got[0].Count != want[0].Count ||
		got[0].Value != 25 || got[0].Count != 2 {
		t.Fatalf("hot override changed: got=%#v want=%#v", got, want)
	}
}

func TestDashboardSeriesMatchesMixedRollupAndRawWindow(t *testing.T) {
	ctx := context.Background()
	store := newRollupStore(t, RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 24 * time.Hour},
		},
	})
	if err := store.CreateMetric(ctx, Definition{Name: "mixed-counter", Type: TypeCounter, RetentionDays: 7}); err != nil {
		t.Fatalf("create mixed metric: %v", err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := store.WriteBatch(ctx, []Point{
		{MetricName: "mixed-counter", EntityID: "node-a", Timestamp: now.Add(-90 * time.Minute), Value: 10},
		{MetricName: "mixed-counter", EntityID: "node-a", Timestamp: now.Add(-60 * time.Minute), Value: 20},
		{MetricName: "mixed-counter", EntityID: "node-b", Timestamp: now.Add(-75 * time.Minute), Value: 30},
		{MetricName: "mixed-counter", EntityID: "node-a", Timestamp: now.Add(-10 * time.Minute), Value: 40},
		{MetricName: "mixed-counter", EntityID: "node-b", Timestamp: now.Add(-5 * time.Minute), Value: 50},
	}); err != nil {
		t.Fatalf("write mixed points: %v", err)
	}
	if _, err := store.Compact(ctx, now); err != nil {
		t.Fatalf("compact mixed points: %v", err)
	}

	query := AggregateQuery{
		Query: Query{
			MetricName: "mixed-counter",
			Start:      now.Add(-2 * time.Hour),
			End:        now,
			Order:      OrderAsc,
		},
		Aggregation:    AggAvg,
		Interval:       time.Hour,
		PreserveSeries: true,
		OmitTags:       true,
	}
	want, err := store.Series(ctx, query, now)
	if err != nil {
		t.Fatalf("query compatibility mixed series: %v", err)
	}
	got, err := store.DashboardSeries(ctx, query, now)
	if err != nil {
		t.Fatalf("query dashboard mixed series: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dashboard mixed series changed semantics\ngot=%#v\nwant=%#v", got, want)
	}
}
