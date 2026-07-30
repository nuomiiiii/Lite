package metric

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestPingSeriesSummaryMatchesExistingSeriesViews(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)
	for _, name := range []string{sqliteMergedPingLatencyMetric, sqliteVirtualPingLossMetric} {
		if err := store.CreateMetric(ctx, Definition{Name: name, Type: TypeGauge, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	values := []float64{12, -1, 28, 16, -1, 33}
	points := make([]Point, 0, len(values))
	for index, value := range values {
		points = append(points, Point{
			MetricName: sqliteMergedPingLatencyMetric,
			EntityID:   "node-a",
			Timestamp:  base.Add(time.Duration(index) * 5 * time.Second),
			Value:      value,
			Tags:       map[string]string{"task_id": "1"},
		})
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	query := AggregateQuery{
		Query: Query{
			MetricName: sqliteMergedPingLatencyMetric,
			EntityID:   "node-a",
			Start:      base,
			End:        base.Add(time.Minute),
			Order:      OrderAsc,
		},
		Interval:       time.Minute,
		PreserveSeries: true,
	}
	summary, err := store.PingSeriesSummary(ctx, query, base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	views := []struct {
		metricName  string
		aggregation Aggregation
		got         []AggregatePoint
	}{
		{sqliteMergedPingLatencyMetric, AggAvg, summary.Avg},
		{sqliteMergedPingLatencyMetric, AggMin, summary.Min},
		{sqliteMergedPingLatencyMetric, AggMax, summary.Max},
		{sqliteMergedPingLatencyMetric, AggLast, summary.Last},
		{sqliteMergedPingLatencyMetric, AggP50, summary.P50},
		{sqliteMergedPingLatencyMetric, AggP99, summary.P99},
		{sqliteMergedPingLatencyMetric, AggStdDev, summary.StdDev},
		{sqliteVirtualPingLossMetric, AggAvg, summary.Loss},
	}
	for _, view := range views {
		legacyQuery := query
		legacyQuery.MetricName = view.metricName
		legacyQuery.Aggregation = view.aggregation
		want, err := store.Series(ctx, legacyQuery, base.Add(time.Minute))
		if err != nil {
			t.Fatalf("legacy %s/%s: %v", view.metricName, view.aggregation, err)
		}
		if !reflect.DeepEqual(view.got, want) {
			t.Fatalf("summary %s/%s mismatch\ngot:  %#v\nwant: %#v", view.metricName, view.aggregation, view.got, want)
		}
	}
}

func TestSQLiteAxisCacheIsBoundedAndCopiesRollupAxes(t *testing.T) {
	cache := newSQLiteAxisCache(320)
	pointKey := sqliteAxisCacheKey{kind: sqliteAxisKindPoint, id: 1, codec: 1, checksum: 1, count: 8}
	cache.add(&sqliteAxisCacheEntry{key: pointKey, timestamps: make([]int64, 8), bytes: 160})
	rollupKey := sqliteAxisCacheKey{kind: sqliteAxisKindRollup, id: 2, codec: 2, checksum: 2, count: 1}
	cache.add(&sqliteAxisCacheEntry{key: rollupKey, records: []sqliteV4RollupRecord{{bucketNano: 42}}, bytes: 160})
	records, ok := cache.rollup(rollupKey)
	if !ok {
		t.Fatal("rollup axis was not cached")
	}
	records[0].bucketNano = 99
	again, ok := cache.rollup(rollupKey)
	if !ok || again[0].bucketNano != 42 {
		t.Fatalf("cached rollup axis was mutated: %#v", again)
	}
	thirdKey := sqliteAxisCacheKey{kind: sqliteAxisKindPoint, id: 3, codec: 1, checksum: 3, count: 8}
	cache.add(&sqliteAxisCacheEntry{key: thirdKey, timestamps: make([]int64, 8), bytes: 160})
	if cache.used > cache.maxBytes {
		t.Fatalf("axis cache exceeded budget: used=%d max=%d", cache.used, cache.maxBytes)
	}
	if _, ok := cache.point(pointKey); ok {
		t.Fatal("least recently used axis was not evicted")
	}
}
