package metric

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestEmptyIncrementalCompactionDoesNotWriteOrChangeSchema(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 48 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 14 * 24 * time.Hour},
		},
	}
	store, err := Open(ctx, SQLiteInDir(t.TempDir(), WithRollupPolicy(policy)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const metricName = "empty-compaction"
	if err := store.CreateMetric(ctx, Definition{Name: metricName, Type: TypeGauge, RetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckpointWAL(ctx); err != nil {
		t.Fatal(err)
	}

	beforeFiles, err := store.SQLiteFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeSchema := sqliteSchemaObjects(t, ctx, store)
	beforePages := sqlitePragmaInt(t, ctx, store.db, "page_count")
	beforeFree := sqlitePragmaInt(t, ctx, store.db, "freelist_count")
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 32; i++ {
		if written, err := store.CompactMetric(ctx, metricName, now.Add(time.Duration(i)*time.Minute)); err != nil || written != 0 {
			t.Fatalf("no-op compaction %d wrote=%d err=%v", i+1, written, err)
		}
	}

	if watermark, found, err := store.compactionWatermark(ctx, metricName); err != nil || found {
		t.Fatalf("empty compaction watermark=%v found=%v err=%v", watermark, found, err)
	}
	afterFiles, err := store.SQLiteFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterFiles != beforeFiles {
		t.Fatalf("empty compaction changed SQLite files: before=%+v after=%+v", beforeFiles, afterFiles)
	}
	if got := sqlitePragmaInt(t, ctx, store.db, "page_count"); got != beforePages {
		t.Fatalf("page_count changed from %d to %d", beforePages, got)
	}
	if got := sqlitePragmaInt(t, ctx, store.db, "freelist_count"); got != beforeFree {
		t.Fatalf("freelist_count changed from %d to %d", beforeFree, got)
	}
	if afterSchema := sqliteSchemaObjects(t, ctx, store); !reflect.DeepEqual(afterSchema, beforeSchema) {
		t.Fatalf("empty compaction changed schema:\nbefore=%v\nafter=%v", beforeSchema, afterSchema)
	}
}

func TestIncrementalCompactionStillProcessesEligibleAndLatePoints(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers:        []RollupTier{{Interval: time.Minute, Retention: 24 * time.Hour}},
	}
	store := newRollupStore(t, policy)
	const metricName = "preflight-late"
	if err := store.CreateMetric(ctx, Definition{Name: metricName, Type: TypeGauge, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if err := store.Write(ctx, Point{MetricName: metricName, EntityID: "node-a", Timestamp: now.Add(-time.Minute), Value: 10}); err != nil {
		t.Fatal(err)
	}
	if written, err := store.CompactMetric(ctx, metricName, now); err != nil || written != 0 {
		t.Fatalf("recent compaction wrote=%d err=%v", written, err)
	}
	if watermark, found, err := store.compactionWatermark(ctx, metricName); err != nil || found {
		t.Fatalf("recent point created watermark=%v found=%v err=%v", watermark, found, err)
	}

	future := now.Add(20 * time.Minute)
	if written, err := store.CompactMetric(ctx, metricName, future); err != nil || written == 0 {
		t.Fatalf("eligible compaction wrote=%d err=%v", written, err)
	}
	rows, err := store.scanRollupRows(ctx, store.reader(), metricName, time.Minute)
	if err != nil || len(rows) != 1 {
		t.Fatalf("eligible rollups=%d err=%v", len(rows), err)
	}
	wantWatermark := policy.rawCutoff(future)
	if watermark, found, err := store.compactionWatermark(ctx, metricName); err != nil || !found || !watermark.Equal(wantWatermark) {
		t.Fatalf("eligible watermark=%v found=%v err=%v want=%v", watermark, found, err, wantWatermark)
	}

	lateTime := now.Add(-2 * time.Hour)
	if err := store.Write(ctx, Point{MetricName: metricName, EntityID: "node-a", Timestamp: lateTime, Value: 20}); err != nil {
		t.Fatal(err)
	}
	if written, err := store.CompactMetric(ctx, metricName, future.Add(time.Minute)); err != nil || written == 0 {
		t.Fatalf("late compaction wrote=%d err=%v", written, err)
	}
	rows, err = store.scanRollupRows(ctx, store.reader(), metricName, time.Minute)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rollups after late point=%d err=%v", len(rows), err)
	}
}

func TestSmallRollupTailDoesNotCauseRepeatedWrites(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers:        []RollupTier{{Interval: time.Minute, Retention: 24 * time.Hour}},
	}
	store, err := Open(ctx, SQLiteInDir(t.TempDir(), WithRollupPolicy(policy)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const metricName = "small-rollup-tail"
	if err := store.CreateMetric(ctx, Definition{Name: metricName, Type: TypeGauge, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if err := store.Write(ctx, Point{MetricName: metricName, EntityID: "node-a", Timestamp: now.Add(-time.Hour), Value: 1}); err != nil {
		t.Fatal(err)
	}
	if written, err := store.CompactMetric(ctx, metricName, now); err != nil || written != 1 {
		t.Fatalf("initial compaction wrote=%d err=%v", written, err)
	}
	if err := store.CheckpointWAL(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := store.SQLiteFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		if written, err := store.CompactMetric(ctx, metricName, now.Add(time.Duration(i)*time.Second)); err != nil || written != 0 {
			t.Fatalf("repeat compaction %d wrote=%d err=%v", i+1, written, err)
		}
	}
	after, err := store.SQLiteFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("small rollup tail changed SQLite files: before=%+v after=%+v", before, after)
	}
}

func sqliteSchemaObjects(t *testing.T, ctx context.Context, store *Store) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, `SELECT type || ':' || name || ':' || COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var object string
		if err := rows.Scan(&object); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return objects
}
