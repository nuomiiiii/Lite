package metric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestCompactionChunksCommitResumeAndPreserveRollups(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: time.Hour,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 48 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 14 * 24 * time.Hour},
			{Interval: time.Hour, Retention: 30 * 24 * time.Hour},
		},
	}
	chunked := newRollupStore(t, policy)
	reference := newRollupStore(t, policy)
	if !chunked.sqliteStorageV4 || !reference.sqliteStorageV4 {
		t.Fatal("chunk regression must exercise SQLite V4 sealed blocks")
	}

	const metricName = "chunked-history"
	base := time.Date(2026, 7, 27, 0, 5, 0, 0, time.UTC)
	now := base.Add(14 * time.Hour)
	rawCutoff := policy.rawCutoff(now)
	points := make([]Point, 13*60)
	for i := range points {
		points[i] = Point{
			MetricName: metricName,
			EntityID:   "node-a",
			Timestamp:  base.Add(time.Duration(i) * time.Minute),
			Value:      float64(i + 1),
			Tags:       map[string]string{"source": "upgrade"},
		}
	}
	for _, store := range []*Store{chunked, reference} {
		if err := store.CreateMetric(ctx, Definition{Name: metricName, Type: TypeGauge, RetentionDays: 30}); err != nil {
			t.Fatalf("create metric: %v", err)
		}
		if err := store.WriteBatch(ctx, points); err != nil {
			t.Fatalf("seed historical points: %v", err)
		}
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.sealSQLiteV4PointsTx(ctx, tx, metricName, now.UnixNano(), 0, 0); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seal historical points: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	effective := policy.withMetricRetention(30 * 24 * time.Hour)
	obsolete := rollupIntervalsOutsidePolicy(policy.Tiers, effective.Tiers)
	written, completed, err := chunked.compactMetricIncrementalChunk(ctx, metricName, now, effective, obsolete)
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if written == 0 || completed {
		t.Fatalf("first chunk wrote=%d completed=%v, want committed partial progress", written, completed)
	}
	firstBoundary := time.Date(2026, 7, 27, 0, 10, 0, 0, time.UTC)
	watermark, ok, err := chunked.compactionWatermark(ctx, metricName)
	if err != nil || !ok || !watermark.Equal(firstBoundary) {
		t.Fatalf("first watermark=%v ok=%v err=%v, want %v", watermark, ok, err, firstBoundary)
	}
	processed, err := chunked.Query(ctx, Query{MetricName: metricName, EntityID: "node-a", Start: base, End: firstBoundary.Add(-time.Nanosecond)})
	if err != nil || len(processed) != 0 {
		t.Fatalf("committed raw chunk still present: count=%d err=%v", len(processed), err)
	}
	pending, err := chunked.Query(ctx, Query{MetricName: metricName, EntityID: "node-a", Start: firstBoundary, End: rawCutoff.Add(-time.Nanosecond)})
	if err != nil || len(pending) == 0 {
		t.Fatalf("unprocessed raw history was lost: count=%d err=%v", len(pending), err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := chunked.compactMetricIncrementalChunk(cancelled, metricName, now, effective, obsolete); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled chunk error=%v, want context.Canceled", err)
	}
	watermark, ok, err = chunked.compactionWatermark(ctx, metricName)
	if err != nil || !ok || !watermark.Equal(firstBoundary) {
		t.Fatalf("cancelled chunk changed watermark=%v ok=%v err=%v", watermark, ok, err)
	}

	if _, err := reference.compactMetricOnce(ctx, metricName, now, effective, obsolete); err != nil {
		t.Fatalf("reference single transaction: %v", err)
	}
	for attempt := 0; attempt < 32; attempt++ {
		if _, err := chunked.CompactMetric(ctx, metricName, now); err != nil {
			t.Fatalf("resume chunked compaction attempt %d: %v", attempt+1, err)
		}
		watermark, ok, err = chunked.compactionWatermark(ctx, metricName)
		if err == nil && ok && watermark.Equal(rawCutoff) {
			break
		}
	}
	if err != nil || !ok || !watermark.Equal(rawCutoff) {
		t.Fatalf("final watermark=%v ok=%v err=%v, want %v", watermark, ok, err, rawCutoff)
	}

	compareRollups := func(stage string) {
		t.Helper()
		for _, aggregation := range []Aggregation{AggCount, AggSum, AggAvg, AggMin, AggMax, AggFirst, AggLast, AggStdDev, AggP50, AggP95, AggP99} {
			query := AggregateQuery{
				Query:       Query{MetricName: metricName, EntityID: "node-a", Start: base.Truncate(time.Hour), End: rawCutoff.Add(-time.Nanosecond)},
				Aggregation: aggregation,
				Interval:    time.Hour,
			}
			got, err := chunked.AggregateRollup(ctx, query, time.Hour)
			if err != nil {
				t.Fatalf("%s chunked %s: %v", stage, aggregation, err)
			}
			want, err := reference.AggregateRollup(ctx, query, time.Hour)
			if err != nil {
				t.Fatalf("%s reference %s: %v", stage, aggregation, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s %s differs after chunk resume:\n got: %#v\nwant: %#v", stage, aggregation, got, want)
			}
		}
	}
	compareRollups("initial")

	late := Point{MetricName: metricName, EntityID: "node-a", Timestamp: base.Add(10 * time.Minute), Value: 9999, Tags: map[string]string{"source": "upgrade"}}
	for _, store := range []*Store{chunked, reference} {
		if err := store.Write(ctx, late); err != nil {
			t.Fatalf("write late point: %v", err)
		}
	}
	previousWatermark := rawCutoff
	if _, completed, err := chunked.compactMetricIncrementalChunk(ctx, metricName, now.Add(time.Minute), effective, obsolete); err != nil || completed {
		t.Fatalf("late chunk completed=%v err=%v, want an old partial range", completed, err)
	}
	watermark, ok, err = chunked.compactionWatermark(ctx, metricName)
	if err != nil || !ok || !watermark.Equal(previousWatermark) {
		t.Fatalf("late chunk regressed watermark=%v ok=%v err=%v, want %v", watermark, ok, err, previousWatermark)
	}
	if _, err := chunked.CompactMetric(ctx, metricName, now.Add(time.Minute)); err != nil {
		t.Fatalf("finish late chunk: %v", err)
	}
	if _, err := reference.compactMetricOnce(ctx, metricName, now.Add(time.Minute), effective, obsolete); err != nil {
		t.Fatalf("reference late compaction: %v", err)
	}
	compareRollups("late point")
}

func TestRuntimeSealingDrainsMoreThanOneSeriesBatch(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 48 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 14 * 24 * time.Hour},
		},
	}
	store := newRollupStore(t, policy)
	if !store.sqliteStorageV4 {
		t.Fatal("batch regression must exercise SQLite V4 sealed blocks")
	}

	const metricName = "many-series"
	if err := store.CreateMetric(ctx, Definition{Name: metricName, Type: TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	now := base.Add(50 * time.Minute)
	points := make([]Point, 0, (metricCompactionSeriesBatch+5)*40)
	for series := 0; series < metricCompactionSeriesBatch+5; series++ {
		for minute := 0; minute < 40; minute++ {
			points = append(points, Point{
				MetricName: metricName,
				EntityID:   fmt.Sprintf("node-%03d", series),
				Timestamp:  base.Add(time.Duration(minute) * time.Minute),
				Value:      float64(series*100 + minute),
			})
		}
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompactMetric(ctx, metricName, now); err != nil {
		t.Fatal(err)
	}

	var sealedSeries int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT series_id) FROM metric_rollup_blocks`).Scan(&sealedSeries); err != nil {
		t.Fatal(err)
	}
	if want := metricCompactionSeriesBatch + 5; sealedSeries != want {
		t.Fatalf("sealed rollup series = %d, want %d", sealedSeries, want)
	}
	var oldHotRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_rollup_values WHERE bucket_nano < ?`, now.Add(-sqliteV4HotWindow).UnixNano()).Scan(&oldHotRows); err != nil {
		t.Fatal(err)
	}
	if oldHotRows != 0 {
		t.Fatalf("eligible hot rollups remained after batched sealing: %d", oldHotRows)
	}
}
