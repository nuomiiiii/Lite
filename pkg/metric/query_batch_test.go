package metric

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestQueryBatchMatchesIndividualQueries(t *testing.T) {
	scanCounts := make(map[string]int)
	ctx := context.Background()
	store := newRollupStore(t, RollupPolicy{})
	for _, definition := range []Definition{
		{Name: "batch.cpu", Type: TypeGauge, RetentionDays: 7},
		{Name: "batch.memory", Type: TypeGauge, RetentionDays: 7},
	} {
		if err := store.CreateMetric(ctx, definition); err != nil {
			t.Fatalf("create metric %s: %v", definition.Name, err)
		}
	}

	base := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	points := make([]Point, 0, 12)
	for metricIndex, metricName := range []string{"batch.cpu", "batch.memory"} {
		for entityIndex, entityID := range []string{"node-a", "node-b"} {
			for sample := 0; sample < 3; sample++ {
				points = append(points, Point{
					MetricName: metricName,
					EntityID:   entityID,
					Timestamp:  base.Add(time.Duration(sample) * time.Minute),
					Value:      float64(metricIndex*100 + entityIndex*10 + sample),
					Tags:       map[string]string{"source": "agent", "device": entityID},
					Labels:     map[string]string{"sample": string(rune('0' + sample))},
				})
			}
		}
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatalf("write points: %v", err)
	}

	queries := []Query{
		{MetricName: "batch.cpu", EntityID: "node-a", Start: base, End: base.Add(2 * time.Minute), Order: OrderAsc},
		{MetricName: "batch.memory", EntityID: "node-b", Start: base, End: base.Add(2 * time.Minute), Order: OrderDesc, Limit: 2},
		{MetricName: "batch.cpu", Start: base, End: base.Add(2 * time.Minute), Tags: map[string]string{"device": "node-b"}, Order: OrderAsc, Offset: 1},
	}
	want := make([][]Point, len(queries))
	for index, query := range queries {
		individual, err := store.Query(ctx, query)
		if err != nil {
			t.Fatalf("individual query %d: %v", index, err)
		}
		want[index] = individual
	}

	observed := withMetricBatchScanObserver(ctx, func(kind string) { scanCounts[kind]++ })
	got, err := store.QueryBatch(observed, queries)
	if err != nil {
		t.Fatalf("query batch: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batch query changed raw semantics\ngot=%#v\nwant=%#v", got, want)
	}
	if got := scanCounts["raw_sqlite_series"]; got != 3 {
		t.Fatalf("series scans = %d, want 3 distinct time/order/tag groups", got)
	}
	if got := scanCounts["raw_sqlite_blocks"]; got != 3 {
		t.Fatalf("block scans = %d, want 3 distinct time/order/tag groups", got)
	}
	if got := scanCounts["raw_sqlite_hot"]; got != 3 {
		t.Fatalf("hot-point scans = %d, want 3 distinct time/order/tag groups", got)
	}
}

func TestQueryBatchCombinesCompatibleMetricsAndEntities(t *testing.T) {
	ctx := context.Background()
	store := newRollupStore(t, RollupPolicy{})
	for _, metricName := range []string{"batch.cpu", "batch.memory"} {
		if err := store.CreateMetric(ctx, Definition{Name: metricName, Type: TypeGauge, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)
	points := make([]Point, 0, 8)
	queries := make([]Query, 0, 4)
	for _, metricName := range []string{"batch.cpu", "batch.memory"} {
		for _, entityID := range []string{"node-a", "node-b"} {
			queries = append(queries, Query{
				MetricName: metricName, EntityID: entityID, Start: base, End: base.Add(time.Minute),
				Tags: map[string]string{"source": "agent"}, Order: OrderAsc,
			})
			for sample := 0; sample < 2; sample++ {
				points = append(points, Point{
					MetricName: metricName, EntityID: entityID,
					Timestamp: base.Add(time.Duration(sample) * time.Minute), Value: float64(sample + 1),
					Tags: map[string]string{"source": "agent"},
				})
			}
		}
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	scanCounts := make(map[string]int)
	observed := withMetricBatchScanObserver(ctx, func(kind string) { scanCounts[kind]++ })
	got, err := store.QueryBatch(observed, queries)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(queries) {
		t.Fatalf("result count = %d, want %d", len(got), len(queries))
	}
	for _, kind := range []string{"raw_sqlite_series", "raw_sqlite_blocks", "raw_sqlite_hot"} {
		if count := scanCounts[kind]; count != 1 {
			t.Fatalf("%s scans = %d, want 1 for four compatible logical queries", kind, count)
		}
	}
}

func TestQueryBatchEmptyInput(t *testing.T) {
	store := newRollupStore(t, RollupPolicy{})
	got, err := store.QueryBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty query batch: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty query batch = %#v, want non-nil empty slice", got)
	}
}
