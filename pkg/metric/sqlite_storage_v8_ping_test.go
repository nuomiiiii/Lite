package metric

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteV8WritesOnePingSeriesAndPreservesPublicQueries(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)
	for _, definition := range []Definition{
		{Name: sqliteMergedPingLatencyMetric, Type: TypeGauge, RetentionDays: 7},
		{Name: sqliteVirtualPingLossMetric, Type: TypeGauge, RetentionDays: 7},
	} {
		if err := store.CreateMetric(ctx, definition); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	latencies := []float64{12, -1, 28, -1}
	points := make([]Point, 0, len(latencies)*2)
	for index, latency := range latencies {
		timestamp := base.Add(time.Duration(index) * 5 * time.Second)
		loss := 0.0
		if latency < 0 {
			loss = 1
		}
		points = append(points,
			Point{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: timestamp, Value: latency, Tags: map[string]string{"task_id": "task-a"}},
			Point{MetricName: sqliteVirtualPingLossMetric, EntityID: "node-a", Timestamp: timestamp, Value: loss, Tags: map[string]string{"task_id": "task-a"}},
		)
	}
	originalNames := make([]string, len(points))
	for index := range points {
		originalNames[index] = points[index].MetricName
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	for index := range points {
		if points[index].MetricName != originalNames[index] {
			t.Fatalf("WriteBatch changed caller point %d from %q to %q", index, originalNames[index], points[index].MetricName)
		}
	}

	var physicalLossSeries int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_series WHERE metric_name = ?`, sqliteVirtualPingLossMetric).Scan(&physicalLossSeries); err != nil {
		t.Fatal(err)
	}
	if physicalLossSeries != 0 {
		t.Fatalf("physical ping.loss series=%d want=0", physicalLossSeries)
	}

	query := Query{EntityID: "node-a", Start: base, End: base.Add(time.Minute), Tags: map[string]string{"task_id": "task-a"}}
	query.MetricName = sqliteVirtualPingLossMetric
	lossPoints, err := store.Query(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	wantLoss := []float64{0, 1, 0, 1}
	if len(lossPoints) != len(wantLoss) {
		t.Fatalf("virtual loss points=%d want=%d", len(lossPoints), len(wantLoss))
	}
	for index := range wantLoss {
		if lossPoints[index].MetricName != sqliteVirtualPingLossMetric || lossPoints[index].Value != wantLoss[index] {
			t.Fatalf("virtual loss point %d=%#v want=%v", index, lossPoints[index], wantLoss[index])
		}
	}
	latest, err := store.Latest(ctx, sqliteVirtualPingLossMetric, "node-a", 1)
	if err != nil || len(latest) != 1 || latest[0].MetricName != sqliteVirtualPingLossMetric || latest[0].Value != 1 {
		t.Fatalf("latest virtual loss=%#v err=%v", latest, err)
	}
	latestBefore, found, err := store.LatestBefore(ctx, sqliteVirtualPingLossMetric, "node-a", base.Add(12*time.Second))
	if err != nil || !found || latestBefore.MetricName != sqliteVirtualPingLossMetric || latestBefore.Value != 0 || !latestBefore.Timestamp.Equal(base.Add(10*time.Second)) {
		t.Fatalf("latest-before virtual loss=%#v found=%v err=%v", latestBefore, found, err)
	}

	assertSQLiteV8Aggregate := func(metricName string, aggregation Aggregation, want float64) {
		t.Helper()
		result, err := store.Aggregate(ctx, AggregateQuery{
			Query:       Query{MetricName: metricName, EntityID: "node-a", Start: base, End: base.Add(time.Minute), Tags: map[string]string{"task_id": "task-a"}},
			Aggregation: aggregation,
			Interval:    time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 || math.Abs(result[0].Value-want) > 1e-12 || result[0].MetricName != metricName {
			t.Fatalf("aggregate %s/%s=%#v want=%v", metricName, aggregation, result, want)
		}
	}
	assertSQLiteV8Aggregate(sqliteMergedPingLatencyMetric, AggAvg, 20)
	assertSQLiteV8Aggregate(sqliteMergedPingLatencyMetric, AggP50, 20)
	assertSQLiteV8Aggregate(sqliteMergedPingLatencyMetric, AggCount, 4)
	assertSQLiteV8Aggregate(sqliteVirtualPingLossMetric, AggAvg, 0.5)
	assertSQLiteV8Aggregate(sqliteVirtualPingLossMetric, AggSum, 2)
	assertSQLiteV8Aggregate(sqliteVirtualPingLossMetric, AggP50, 0.5)
	statsQuery := query
	statsQuery.MetricName = sqliteMergedPingLatencyMetric
	stats, err := store.Stats(ctx, statsQuery)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 4 || stats.Avg != 20 || stats.Min != 12 || stats.Max != 28 || stats.P50 != 20 || stats.StdDev != 8 || stats.First != 12 || stats.Last != -1 {
		t.Fatalf("merged Ping latency stats included loss sentinels: %#v", stats)
	}
}

func TestSQLiteV8MigratesLegacyDualPingPointsAtomically(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	legacy := createSQLiteV8LegacyStore(t, ctx, dsn)
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for _, definition := range []Definition{
		{Name: sqliteMergedPingLatencyMetric, Type: TypeGauge, RetentionDays: 19},
		{Name: sqliteVirtualPingLossMetric, Type: TypeGauge, RetentionDays: 19},
	} {
		if err := legacy.CreateMetric(ctx, definition); err != nil {
			t.Fatal(err)
		}
	}
	points := []Point{
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: base, Value: 21},
		{MetricName: sqliteVirtualPingLossMetric, EntityID: "node-a", Timestamp: base, Value: 0},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: base.Add(5 * time.Second), Value: -1},
		{MetricName: sqliteVirtualPingLossMetric, EntityID: "node-a", Timestamp: base.Add(5 * time.Second), Value: 1},
	}
	if err := legacy.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	var phases []string
	store, err := Open(ctx, SQLite(dsn, WithMigrationProgress(func(progress MigrationProgress) {
		phases = append(phases, progress.Phase)
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.sqlitePingMerged || !containsString(phases, MigrationPhaseMergingPingSeries) {
		t.Fatalf("V8 migration state merged=%v phases=%v", store.sqlitePingMerged, phases)
	}
	var userVersion, physicalLossSeries int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_series WHERE metric_name = ?`, sqliteVirtualPingLossMetric).Scan(&physicalLossSeries); err != nil {
		t.Fatal(err)
	}
	if userVersion != sqliteStorageVersionPingMerge || physicalLossSeries != 0 {
		t.Fatalf("migrated version=%d loss_series=%d", userVersion, physicalLossSeries)
	}
	definition, err := store.GetMetric(ctx, sqliteMergedPingLatencyMetric)
	if err != nil || definition.RetentionDays != 19 {
		t.Fatalf("migration changed retention: definition=%#v err=%v", definition, err)
	}
	loss, err := store.Query(ctx, Query{MetricName: sqliteVirtualPingLossMetric, EntityID: "node-a", Start: base, End: base.Add(time.Minute)})
	if err != nil || len(loss) != 2 || loss[0].Value != 0 || loss[1].Value != 1 {
		t.Fatalf("migrated virtual loss=%#v err=%v", loss, err)
	}
}

func TestSQLiteV8MigrationMismatchRollsBackPingMerge(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	legacy := createSQLiteV8LegacyStore(t, ctx, dsn)
	base := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{sqliteMergedPingLatencyMetric, sqliteVirtualPingLossMetric} {
		if err := legacy.CreateMetric(ctx, Definition{Name: name, Type: TypeGauge, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.WriteBatch(ctx, []Point{
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: base, Value: 20},
		{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: base.Add(5 * time.Second), Value: -1},
		{MetricName: sqliteVirtualPingLossMetric, EntityID: "node-a", Timestamp: base, Value: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	if migrated, err := Open(ctx, SQLite(dsn)); err == nil {
		_ = migrated.Close()
		t.Fatal("mismatched Ping series unexpectedly migrated")
	} else if !strings.Contains(err.Error(), "ping point count mismatch") {
		t.Fatalf("unexpected migration error: %v", err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var userVersion, latencySeries, lossSeries int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_series WHERE metric_name = ?`, sqliteMergedPingLatencyMetric).Scan(&latencySeries); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_series WHERE metric_name = ?`, sqliteVirtualPingLossMetric).Scan(&lossSeries); err != nil {
		t.Fatal(err)
	}
	if userVersion >= sqliteStorageVersionPingMerge || latencySeries != 1 || lossSeries != 1 {
		t.Fatalf("failed migration changed Ping data: version=%d latency=%d loss=%d", userVersion, latencySeries, lossSeries)
	}
}

func TestSQLiteV8MigratesLegacyPingRollupsWithoutRawPoints(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	legacy := createSQLiteV8LegacyStore(t, ctx, dsn)
	for _, name := range []string{sqliteMergedPingLatencyMetric, sqliteVirtualPingLossMetric} {
		if err := legacy.CreateMetric(ctx, Definition{Name: name, Type: TypeGauge, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}
	bucket := time.Now().UTC().Add(-time.Hour).Truncate(time.Minute)
	if err := insertSQLiteV8LegacyPingRollups(ctx, legacy, bucket, 2); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, SQLite(dsn, WithRollupPolicy(RollupPolicy{
		RawRetention: 15 * time.Minute,
		Tiers:        []RollupTier{{Interval: time.Minute, Retention: 24 * time.Hour}},
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.scanRollupRowsBetween(ctx, sqliteMergedPingLatencyMetric, "node-a", map[string]string{"task_id": "task-a"},
		time.Minute.Nanoseconds(), bucket.UnixNano(), bucket.UnixNano(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("migrated latency rollups=%d want=1", len(rows))
	}
	got := rows[0].bucketData
	if got.count != 4 || got.lossCount != 2 || got.sum != 40 || got.sumSq != 1000 || got.min != 10 || got.max != 30 {
		t.Fatalf("migrated latency rollup changed: %#v", got)
	}
	if value, ok := got.value(aggPingLossAvg); !ok || value != 0.5 {
		t.Fatalf("migrated virtual loss average=%v ok=%v", value, ok)
	}
	var lossSeries int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_series WHERE metric_name = ?`, sqliteVirtualPingLossMetric).Scan(&lossSeries); err != nil {
		t.Fatal(err)
	}
	if lossSeries != 0 {
		t.Fatalf("legacy rollup migration retained %d physical loss series", lossSeries)
	}
}

func TestSQLiteV8LegacyPingRollupMismatchRollsBack(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	legacy := createSQLiteV8LegacyStore(t, ctx, dsn)
	for _, name := range []string{sqliteMergedPingLatencyMetric, sqliteVirtualPingLossMetric} {
		if err := legacy.CreateMetric(ctx, Definition{Name: name, Type: TypeGauge, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertSQLiteV8LegacyPingRollups(ctx, legacy, time.Now().UTC().Add(-time.Hour).Truncate(time.Minute), 3); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	if migrated, err := Open(ctx, SQLite(dsn)); err == nil {
		_ = migrated.Close()
		t.Fatal("inconsistent legacy Ping rollup unexpectedly migrated")
	} else if !strings.Contains(err.Error(), "loss summary square count is inconsistent") {
		t.Fatalf("unexpected migration error: %v", err)
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var userVersion, lossSeries int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_series WHERE metric_name = ?`, sqliteVirtualPingLossMetric).Scan(&lossSeries); err != nil {
		t.Fatal(err)
	}
	if userVersion >= sqliteStorageVersionPingMerge || lossSeries != 1 {
		t.Fatalf("failed rollup migration changed storage: version=%d loss_series=%d", userVersion, lossSeries)
	}
}

func TestSQLiteV8RollupConversionAndCodecAreLossless(t *testing.T) {
	digest := NewTDigest(defaultTDigestCompression)
	for _, value := range []float64{-1, 10, 30, -1} {
		digest.Add(value, 1)
	}
	record := sqliteV4RollupRecord{
		bucketNano: 100,
		count:      4,
		sumBits:    math.Float64bits(38),
		sumSqBits:  math.Float64bits(1002),
		minBits:    math.Float64bits(-1),
		maxBits:    math.Float64bits(30),
		firstBits:  math.Float64bits(-1),
		firstTS:    100,
		lastBits:   math.Float64bits(-1),
		lastTS:     103,
		digest:     digest.Encode(),
		createdAt:  200,
	}
	converted, err := convertSQLiteV8LatencyRollup(record, 2)
	if err != nil {
		t.Fatal(err)
	}
	if converted.lossCount != 2 || math.Float64frombits(converted.sumBits) != 40 || math.Float64frombits(converted.sumSqBits) != 1000 ||
		math.Float64frombits(converted.minBits) != 10 || math.Float64frombits(converted.maxBits) != 30 || converted.firstBits != record.firstBits || converted.lastBits != record.lastBits {
		t.Fatalf("unexpected converted rollup: %#v", converted)
	}
	encoded, err := encodeSQLiteV4RollupBlock([]sqliteV4RollupRecord{converted})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSQLiteV4EncodedRollupBlock(encoded, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || !sqliteV4RollupRecordDataEqual(converted, decoded[0]) {
		t.Fatal("SQLite V8 rollup codec changed a summary or loss count")
	}
}

func TestSQLiteV8PingLossCountSurvivesTierHandoff(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: 10 * time.Minute,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 20 * time.Minute},
			{Interval: 5 * time.Minute, Retention: 2 * time.Hour},
			{Interval: time.Hour, Retention: 24 * time.Hour},
		},
	}
	store, err := Open(ctx, SQLiteInDir(t.TempDir(), WithRollupPolicy(policy)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, definition := range []Definition{
		{Name: sqliteMergedPingLatencyMetric, Type: TypeGauge, RetentionDays: 1},
		{Name: sqliteVirtualPingLossMetric, Type: TypeGauge, RetentionDays: 1},
	} {
		if err := store.CreateMetric(ctx, definition); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC().Truncate(time.Minute)
	base := now.Add(-90 * time.Minute).Truncate(5 * time.Minute).Add(time.Minute)
	latencies := []float64{10, -1, 30, -1}
	points := make([]Point, 0, len(latencies)*2)
	for index, latency := range latencies {
		timestamp := base.Add(time.Duration(index) * 5 * time.Second)
		loss := 0.0
		if latency < 0 {
			loss = 1
		}
		points = append(points,
			Point{MetricName: sqliteMergedPingLatencyMetric, EntityID: "node-a", Timestamp: timestamp, Value: latency, Tags: map[string]string{"task_id": "task-a"}},
			Point{MetricName: sqliteVirtualPingLossMetric, EntityID: "node-a", Timestamp: timestamp, Value: loss, Tags: map[string]string{"task_id": "task-a"}},
		)
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compact(ctx, now); err != nil {
		t.Fatal(err)
	}

	var minuteBuckets, fiveMinuteBuckets int
	if err := store.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM metric_rollup_blocks b JOIN metric_series s ON s.id = b.series_id WHERE s.metric_name = ? AND b.resolution_nano = ?) +
		(SELECT COUNT(*) FROM metric_rollup_values v JOIN metric_series s ON s.id = v.series_id WHERE s.metric_name = ? AND v.resolution_nano = ?)`,
		sqliteMergedPingLatencyMetric, time.Minute.Nanoseconds(), sqliteMergedPingLatencyMetric, time.Minute.Nanoseconds()).Scan(&minuteBuckets); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM metric_rollup_blocks b JOIN metric_series s ON s.id = b.series_id WHERE s.metric_name = ? AND b.resolution_nano = ?) +
		(SELECT COUNT(*) FROM metric_rollup_values v JOIN metric_series s ON s.id = v.series_id WHERE s.metric_name = ? AND v.resolution_nano = ?)`,
		sqliteMergedPingLatencyMetric, (5 * time.Minute).Nanoseconds(), sqliteMergedPingLatencyMetric, (5 * time.Minute).Nanoseconds()).Scan(&fiveMinuteBuckets); err != nil {
		t.Fatal(err)
	}
	if minuteBuckets != 0 || fiveMinuteBuckets == 0 {
		t.Fatalf("tier handoff retained duplicate buckets: one_minute=%d five_minute=%d", minuteBuckets, fiveMinuteBuckets)
	}

	seriesQuery := AggregateQuery{
		Query:       Query{EntityID: "node-a", Start: base.Truncate(5 * time.Minute), End: base.Add(5 * time.Minute), Tags: map[string]string{"task_id": "task-a"}},
		Aggregation: AggAvg,
		Interval:    5 * time.Minute,
	}
	seriesQuery.MetricName = sqliteMergedPingLatencyMetric
	latency, err := store.Series(ctx, seriesQuery, now)
	if err != nil || len(latency) != 1 || latency[0].Value != 20 {
		t.Fatalf("handoff latency=%#v err=%v", latency, err)
	}
	seriesQuery.MetricName = sqliteVirtualPingLossMetric
	loss, err := store.Series(ctx, seriesQuery, now)
	if err != nil || len(loss) != 1 || loss[0].Value != 0.5 || loss[0].MetricName != sqliteVirtualPingLossMetric {
		t.Fatalf("handoff loss=%#v err=%v", loss, err)
	}
}

func createSQLiteV8LegacyStore(t *testing.T, ctx context.Context, dsn string) *Store {
	t.Helper()
	store, err := Open(ctx, SQLite(dsn, WithAutoMigrate(false)))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.migrateSQLiteStorageV3(ctx, true); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store
}

func insertSQLiteV8LegacyPingRollups(ctx context.Context, store *Store, bucket time.Time, lossSum float64) error {
	tagsHash, tagsJSON, err := tagsFingerprint(map[string]string{"task_id": "task-a"})
	if err != nil {
		return err
	}
	latencyDigest := NewTDigest(defaultTDigestCompression)
	lossDigest := NewTDigest(defaultTDigestCompression)
	for _, value := range []float64{10, -1, 30, -1} {
		latencyDigest.Add(value, 1)
	}
	for _, value := range []float64{0, 1, 0, 1} {
		lossDigest.Add(value, 1)
	}
	statement := `INSERT INTO metric_rollups
		(metric_name, entity_id, tags_hash, tags, resolution_nano, bucket_nano, count,
		 sum, sum_sq, min_val, max_val, first_val, first_ts, last_val, last_ts, digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	firstTS := bucket.UnixNano()
	lastTS := bucket.Add(15 * time.Second).UnixNano()
	createdAt := bucket.Add(time.Minute).UnixNano()
	if _, err := store.db.ExecContext(ctx, statement,
		sqliteMergedPingLatencyMetric, "node-a", tagsHash, tagsJSON, time.Minute.Nanoseconds(), bucket.UnixNano(), 4,
		38.0, 1002.0, -1.0, 30.0, 10.0, firstTS, -1.0, lastTS, latencyDigest.Encode(), createdAt,
	); err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, statement,
		sqliteVirtualPingLossMetric, "node-a", tagsHash, tagsJSON, time.Minute.Nanoseconds(), bucket.UnixNano(), 4,
		lossSum, 2.0, 0.0, 1.0, 0.0, firstTS, 1.0, lastTS, lossDigest.Encode(), createdAt,
	)
	return err
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
