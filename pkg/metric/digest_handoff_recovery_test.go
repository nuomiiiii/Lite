package metric

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestSQLiteV4DigestHandoffAcceptsAssociativeRoundingDrift(t *testing.T) {
	stored := &rollupBucket{
		count:    150,
		sum:      math.Float64frombits(4645082735656904392),
		sumSq:    math.Float64frombits(4659346703785820420),
		min:      0.001,
		max:      19.240506329576526,
		firstVal: 1.4999999956635293,
		firstTS:  1785042601405271820,
		lastVal:  0.7499999949213816,
		lastTS:   1785042899404025910,
	}
	rebuilt := *stored
	rebuilt.sumSq = math.Float64frombits(math.Float64bits(stored.sumSq) - 2)
	if !sqliteV4RollupSummariesEqual(&rebuilt, stored) {
		t.Fatal("two-ULP accumulation drift from the supplied backup should be accepted")
	}

	rebuilt.sumSq = math.Float64frombits(math.Float64bits(stored.sumSq) - 3)
	if sqliteV4RollupSummariesEqual(&rebuilt, stored) {
		t.Fatal("a drift beyond the verified bound should still defer handoff")
	}
	rebuilt = *stored
	rebuilt.lastTS--
	if sqliteV4RollupSummariesEqual(&rebuilt, stored) {
		t.Fatal("sample identity fields must remain exact")
	}
}

func TestSQLiteStorageV4SkipsExpiredDigestHandoffAndRestoresLaterBucket(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: time.Hour,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 2 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 24 * time.Hour},
		},
	}
	var snapshots []MigrationProgress
	store, err := Open(ctx, SQLiteInDir(t.TempDir(), WithRollupPolicy(policy), WithMigrationProgress(func(progress MigrationProgress) {
		snapshots = append(snapshots, progress)
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateMetric(ctx, Definition{Name: "gpu.usage", Type: TypeGauge, RetentionDays: 10}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)
	start := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	allFine := make(map[rollupKey]*rollupBucket)
	for minute := 0; minute < 10; minute++ {
		bucketTime := start.Add(time.Duration(minute) * time.Minute)
		bucket := newRollupBucket(policy.compression())
		for sample := 0; sample < 3; sample++ {
			bucket.addPoint(float64(minute*10+sample), bucketTime.Add(time.Duration(sample)*time.Second).UnixNano())
		}
		allFine[rollupKey{entityID: "node-a", bucket: bucketTime.UnixNano()}] = bucket
	}
	coarse := buildCoarserBucketsFromDelta(allFine, 5*time.Minute, policy.compression())
	partialFine := make(map[rollupKey]*rollupBucket)
	missingBucket := start.Add(4 * time.Minute).UnixNano()
	for key, bucket := range allFine {
		if key.bucket != missingBucket {
			partialFine[key] = bucket
		}
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writeRollupBucketsTx(ctx, "gpu.usage", time.Minute, partialFine, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := store.writeRollupBucketsTx(ctx, "gpu.usage", 5*time.Minute, coarse, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := store.sealAllSQLiteV4RollupsTx(ctx, tx, 0, 0); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	series, err := store.sqliteV4MatchingSeries(ctx, store.db, "gpu.usage", "node-a", nil)
	if err != nil || len(series) != 1 {
		t.Fatalf("find gpu series: count=%d err=%v", len(series), err)
	}
	records, err := store.loadAllSQLiteV4RollupBlockRecords(ctx, store.db, series[0].id, (5 * time.Minute).Nanoseconds())
	if err != nil || len(records) != 2 {
		t.Fatalf("load coarse rollup: count=%d err=%v", len(records), err)
	}
	for index := range records {
		records[index].digest = nil
	}
	encoded, err := encodeSQLiteV4RollupBlock(records)
	if err != nil {
		t.Fatal(err)
	}
	updateSQL := fmt.Sprintf("UPDATE %s SET end_nano = ?, bucket_count = ?, codec = ?, checksum = ?, payload = ?, digest_codec = ?, digest_checksum = ?, digest_payload = ? WHERE series_id = ? AND resolution_nano = ? AND start_nano = ?", store.tables.rollupBlocks)
	if _, err := store.db.ExecContext(ctx, updateSQL,
		encoded.endNano, encoded.count, encoded.codec, int64(encoded.checksum), encoded.payload,
		encoded.digestCodec, int64(encoded.digestChecksum), encoded.digestPayload,
		series[0].id, (5 * time.Minute).Nanoseconds(), encoded.startNano); err != nil {
		t.Fatal(err)
	}

	beforeExpired := records[0]

	rewrittenBlocks, rewrittenBuckets, err := store.migrateSQLiteV4RedundantRollupDigests(ctx, now, true)
	if err != nil {
		t.Fatalf("incomplete historical digest handoff blocked startup migration: %v", err)
	}
	if rewrittenBlocks != 1 || rewrittenBuckets != 2 {
		t.Fatalf("later recoverable handoff was not rewritten: blocks=%d buckets=%d", rewrittenBlocks, rewrittenBuckets)
	}
	after, err := store.loadAllSQLiteV4RollupBlockRecords(ctx, store.db, series[0].id, (5 * time.Minute).Nanoseconds())
	if err != nil || len(after) != 2 {
		t.Fatalf("load migrated coarse rollup: count=%d err=%v", len(after), err)
	}
	if !sqliteV4RollupRecordDataEqual(beforeExpired, after[0]) || len(after[0].digest) != 0 {
		t.Fatal("expired handoff changed the readable coarse summary")
	}
	if len(after[1].digest) == 0 {
		t.Fatal("expired handoff prevented a later complete bucket from being restored")
	}
	for _, snapshot := range snapshots {
		if snapshot.Deferred != 0 {
			t.Fatalf("expired handoff was incorrectly reported as deferred: %#v", snapshot)
		}
	}
	definition, err := store.GetMetric(ctx, "gpu.usage")
	if err != nil || definition.RetentionDays != 10 {
		t.Fatalf("incomplete handoff changed retention: days=%d err=%v", definition.RetentionDays, err)
	}
}

func TestSQLiteStorageV4DeferredHandoffClearsWatermarkAndProtectsFineData(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: time.Hour,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 2 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 24 * time.Hour},
		},
	}
	var snapshots []MigrationProgress
	store, err := Open(ctx, SQLiteInDir(t.TempDir(), WithRollupPolicy(policy), WithMigrationProgress(func(progress MigrationProgress) {
		snapshots = append(snapshots, progress)
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateMetric(ctx, Definition{Name: "cpu.usage", Type: TypeGauge, RetentionDays: 10}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)
	start := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	fine := make(map[rollupKey]*rollupBucket)
	for minute := 0; minute < 5; minute++ {
		bucketTime := start.Add(time.Duration(minute) * time.Minute)
		bucket := newRollupBucket(policy.compression())
		for sample := 0; sample < 3; sample++ {
			bucket.addPoint(float64(minute*10+sample), bucketTime.Add(time.Duration(sample)*time.Second).UnixNano())
		}
		fine[rollupKey{entityID: "node-a", bucket: bucketTime.UnixNano()}] = bucket
	}
	coarse := buildCoarserBucketsFromDelta(fine, 5*time.Minute, policy.compression())

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.writeRollupBucketsTx(ctx, "cpu.usage", time.Minute, fine, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := store.writeRollupBucketsTx(ctx, "cpu.usage", 5*time.Minute, coarse, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := store.sealAllSQLiteV4RollupsTx(ctx, tx, 0, 0); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	series, err := store.sqliteV4MatchingSeries(ctx, store.db, "cpu.usage", "node-a", nil)
	if err != nil || len(series) != 1 {
		t.Fatalf("find cpu series: count=%d err=%v", len(series), err)
	}
	coarseRecords, err := store.loadAllSQLiteV4RollupBlockRecords(ctx, store.db, series[0].id, (5 * time.Minute).Nanoseconds())
	if err != nil || len(coarseRecords) != 1 {
		t.Fatalf("load coarse rollup: count=%d err=%v", len(coarseRecords), err)
	}
	coarseRecords[0].digest = nil
	coarseRecords[0].sumBits = math.Float64bits(math.Float64frombits(coarseRecords[0].sumBits) + 1)
	encoded, err := encodeSQLiteV4RollupBlock(coarseRecords)
	if err != nil {
		t.Fatal(err)
	}
	updateSQL := fmt.Sprintf("UPDATE %s SET end_nano = ?, bucket_count = ?, codec = ?, checksum = ?, payload = ?, digest_codec = ?, digest_checksum = ?, digest_payload = ? WHERE series_id = ? AND resolution_nano = ? AND start_nano = ?", store.tables.rollupBlocks)
	if _, err := store.db.ExecContext(ctx, updateSQL,
		encoded.endNano, encoded.count, encoded.codec, int64(encoded.checksum), encoded.payload,
		encoded.digestCodec, int64(encoded.digestChecksum), encoded.digestPayload,
		series[0].id, (5 * time.Minute).Nanoseconds(), encoded.startNano); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (metric_name, watermark_nano, updated_at) VALUES (?, ?, ?)", store.tables.watermarks),
		"cpu.usage", policy.rawCutoff(now).UnixNano(), now.UnixNano()); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.migrateSQLiteV4RedundantRollupDigests(ctx, now, true); err != nil {
		t.Fatalf("deferred startup handoff must not fail migration: %v", err)
	}
	deferredReported := false
	for _, snapshot := range snapshots {
		deferredReported = deferredReported || snapshot.Deferred > 0
	}
	if !deferredReported {
		t.Fatal("a complete but mismatched source was not reported as deferred")
	}
	if _, found, err := store.compactionWatermark(ctx, "cpu.usage"); err != nil || found {
		t.Fatalf("deferred startup handoff kept compaction watermark: found=%v err=%v", found, err)
	}
	fineBefore, err := store.loadAllSQLiteV4RollupBlockRecords(ctx, store.db, series[0].id, time.Minute.Nanoseconds())
	if err != nil || len(fineBefore) != 5 {
		t.Fatalf("load fine rollups before retry: count=%d err=%v", len(fineBefore), err)
	}
	if _, err := store.CompactMetric(ctx, "cpu.usage", now); !IsDigestHandoffDeferred(err) {
		t.Fatalf("runtime compaction did not retry deferred handoff: %v", err)
	}
	fineAfter, err := store.loadAllSQLiteV4RollupBlockRecords(ctx, store.db, series[0].id, time.Minute.Nanoseconds())
	if err != nil || !sqliteV4RollupRecordDataSlicesEqual(fineBefore, fineAfter) {
		t.Fatalf("deferred retry deleted or changed fine rollups: before=%d after=%d err=%v", len(fineBefore), len(fineAfter), err)
	}
}
