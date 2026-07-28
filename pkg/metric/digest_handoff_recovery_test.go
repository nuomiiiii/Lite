package metric

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSQLiteStorageV4DefersIncompleteDigestHandoffWithoutChangingBlock(t *testing.T) {
	ctx := context.Background()
	policy := RollupPolicy{
		RawRetention: time.Hour,
		Tiers: []RollupTier{
			{Interval: time.Minute, Retention: 2 * time.Hour},
			{Interval: 5 * time.Minute, Retention: 24 * time.Hour},
		},
	}
	store, err := Open(ctx, SQLiteInDir(t.TempDir(), WithRollupPolicy(policy)))
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
	for minute := 0; minute < 5; minute++ {
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
	if err != nil || len(records) != 1 {
		t.Fatalf("load coarse rollup: count=%d err=%v", len(records), err)
	}
	records[0].digest = nil
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

	type blockSnapshot struct {
		endNano, count, codec, checksum, digestCodec, digestChecksum int64
		payload, digestPayload                                       []byte
	}
	readBlock := func() blockSnapshot {
		var got blockSnapshot
		querySQL := fmt.Sprintf("SELECT end_nano, bucket_count, codec, checksum, payload, digest_codec, digest_checksum, digest_payload FROM %s WHERE series_id = ? AND resolution_nano = ? AND start_nano = ?", store.tables.rollupBlocks)
		err := store.db.QueryRowContext(ctx, querySQL,
			series[0].id, (5*time.Minute).Nanoseconds(), encoded.startNano).Scan(
			&got.endNano, &got.count, &got.codec, &got.checksum, &got.payload,
			&got.digestCodec, &got.digestChecksum, &got.digestPayload,
		)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := readBlock()

	rewrittenBlocks, rewrittenBuckets, err := store.migrateSQLiteV4RedundantRollupDigests(ctx, now, true)
	if err != nil {
		t.Fatalf("incomplete historical digest handoff blocked startup migration: %v", err)
	}
	if rewrittenBlocks != 0 || rewrittenBuckets != 0 {
		t.Fatalf("incomplete handoff rewrote data: blocks=%d buckets=%d", rewrittenBlocks, rewrittenBuckets)
	}
	after := readBlock()
	if before.endNano != after.endNano || before.count != after.count || before.codec != after.codec ||
		before.checksum != after.checksum || before.digestCodec != after.digestCodec ||
		before.digestChecksum != after.digestChecksum || !bytes.Equal(before.payload, after.payload) ||
		!bytes.Equal(before.digestPayload, after.digestPayload) {
		t.Fatal("incomplete handoff changed the stored rollup block")
	}
	definition, err := store.GetMetric(ctx, "gpu.usage")
	if err != nil || definition.RetentionDays != 10 {
		t.Fatalf("incomplete handoff changed retention: days=%d err=%v", definition.RetentionDays, err)
	}
}
