package metric

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

// handoffExpiredRollupTiersTx moves expired buckets down the retention ladder.
// A finer bucket is deleted only after its complete summary and digest have
// been merged into the next tier in the same transaction.
func (s *Store) handoffExpiredRollupTiersTx(ctx context.Context, metricName string, now time.Time, policy RollupPolicy, tx *sql.Tx) (int, error) {
	if len(policy.Tiers) == 0 {
		return 0, nil
	}
	comp := policy.compression()
	written := 0
	for index := 0; index+1 < len(policy.Tiers); index++ {
		fine := policy.Tiers[index]
		coarse := policy.Tiers[index+1]
		cutoff := alignRollupRetentionCutoff(now.Add(-fine.Retention), coarse.Interval)
		upper := cutoff.UnixNano()
		if upper == math.MinInt64 {
			continue
		}
		rows, err := s.scanRollupRowsRange(ctx, tx, metricName, fine.Interval, math.MinInt64, upper-1, true)
		if err != nil {
			return written, fmt.Errorf("metric: read %s tier before handoff: %w", fine.Interval, err)
		}
		if len(rows) == 0 {
			continue
		}
		for _, row := range rows {
			if row.bucketData.digest == nil && !rollupDigestOptional(metricName, row.bucketData) {
				return written, fmt.Errorf("metric: cannot losslessly hand off %s bucket %d with a missing digest", fine.Interval, row.bucket)
			}
		}
		buckets := coarsenStoredRollups(metricName, rows, coarse.Interval, comp)
		n, err := s.mergeRollupBucketsTx(ctx, metricName, coarse.Interval, buckets, tx)
		if err != nil {
			return written, fmt.Errorf("metric: hand off %s tier to %s: %w", fine.Interval, coarse.Interval, err)
		}
		written += n
		if err := s.deleteRollupsBeforeTx(ctx, metricName, fine.Interval, cutoff, tx); err != nil {
			return written, fmt.Errorf("metric: remove handed-off %s tier: %w", fine.Interval, err)
		}
	}

	last := policy.Tiers[len(policy.Tiers)-1]
	lastCutoff := alignRollupRetentionCutoff(now.Add(-last.Retention), last.Interval)
	if err := s.deleteRollupsBeforeTx(ctx, metricName, last.Interval, lastCutoff, tx); err != nil {
		return written, fmt.Errorf("metric: expire final %s tier: %w", last.Interval, err)
	}
	return written, nil
}

func coarsenStoredRollups(metricName string, rows []storedRollup, interval time.Duration, comp float64) map[rollupKey]*rollupBucket {
	out := make(map[rollupKey]*rollupBucket)
	coarseNano := interval.Nanoseconds()
	for _, row := range rows {
		key := rollupKey{
			entityID: row.entityID,
			tagsHash: row.bucketData.tagsHash,
			bucket:   floorDivNano(row.bucket, coarseNano),
		}
		bucket := out[key]
		if bucket == nil {
			// Start without a digest and let mergeStored create one only when a
			// finer bucket has real latency samples. An all-loss Ping group then
			// remains a legitimate digest-free bucket at the coarser tier.
			bucket = newRollupBucketWithDigest(comp, false)
			bucket.tagsHash = row.bucketData.tagsHash
			bucket.tagsJSON = row.bucketData.tagsJSON
			out[key] = bucket
		}
		bucket.mergeStored(row.bucketData)
	}
	for _, bucket := range out {
		if rollupDigestOptional(metricName, bucket) {
			bucket.digest = nil
		}
	}
	return out
}

func (s *Store) scanRollupRowsRange(ctx context.Context, q querier, metricName string, interval time.Duration, lower, upper int64, needDigest bool) ([]storedRollup, error) {
	if upper < lower {
		return nil, nil
	}
	if s.sqliteStorageV4 {
		return s.querySQLiteV4Rollups(ctx, q, metricName, "", nil, interval.Nanoseconds(), lower, upper, needDigest)
	}
	columns := "entity_id, tags_hash, tags, bucket_nano, count, sum, sum_sq, min_val, max_val, first_val, first_ts, last_val, last_ts"
	if needDigest {
		columns += ", digest"
	}
	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE metric_name = %s AND resolution_nano = %s AND bucket_nano >= %s AND bucket_nano <= %s ORDER BY bucket_nano ASC",
		columns, s.tables.rollups,
		s.dialect.placeholder(1), s.dialect.placeholder(2), s.dialect.placeholder(3), s.dialect.placeholder(4),
	)
	rows, err := q.QueryContext(ctx, query, metricName, interval.Nanoseconds(), lower, upper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStoredRollups(rows, needDigest)
}

func (s *Store) deleteRollupsAtOrAfterTx(ctx context.Context, metricName string, interval time.Duration, at time.Time, tx *sql.Tx) error {
	if s.sqliteStorageV4 {
		return s.rewriteSQLiteV4RollupRangeTx(ctx, tx, metricName, interval.Nanoseconds(), math.MinInt64, at.UTC().UnixNano()-1)
	}
	query := fmt.Sprintf(
		`DELETE FROM %s WHERE metric_name = %s AND resolution_nano = %s AND bucket_nano >= %s`,
		s.tables.rollups, s.dialect.placeholder(1), s.dialect.placeholder(2), s.dialect.placeholder(3),
	)
	_, err := tx.ExecContext(ctx, query, metricName, interval.Nanoseconds(), at.UTC().UnixNano())
	return err
}

// rewriteSQLiteV4RollupRangeTx retains only buckets in the inclusive range.
// It is used during the one-time transition from overlapping tiers; processing
// one series at a time bounds memory while every rewrite remains transactional.
func (s *Store) rewriteSQLiteV4RollupRangeTx(ctx context.Context, tx *sql.Tx, metricName string, resolution, keepLower, keepUpper int64) error {
	series, err := s.sqliteV4MatchingSeries(ctx, tx, metricName, "", nil)
	if err != nil {
		return err
	}
	for _, item := range series {
		blockRecords, err := s.loadAllSQLiteV4RollupBlockRecords(ctx, tx, item.id, resolution)
		if err != nil {
			return err
		}
		byBucket := make(map[int64]sqliteV4RollupRecord, len(blockRecords))
		for _, record := range blockRecords {
			byBucket[record.bucketNano] = record
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT bucket_nano, count, loss_count, sum, sum_sq, min_val, max_val, first_val, first_ts,
			        last_val, last_ts, digest, created_at FROM %s
			 WHERE series_id = ? AND resolution_nano = ? ORDER BY bucket_nano`, s.tables.rollupValues,
		), item.id, resolution)
		if err != nil {
			return err
		}
		for rows.Next() {
			record, scanErr := scanSQLiteV4RollupRecord(rows)
			if scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			byBucket[record.bucketNano] = record
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		kept := make([]sqliteV4RollupRecord, 0, len(byBucket))
		for _, record := range byBucket {
			if record.bucketNano >= keepLower && record.bucketNano <= keepUpper {
				kept = append(kept, record)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.tables.rollupBlocks+` WHERE series_id = ? AND resolution_nano = ?`, item.id, resolution); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.tables.rollupValues+` WHERE series_id = ? AND resolution_nano = ?`, item.id, resolution); err != nil {
			return err
		}
		if err := s.writeSQLiteV4RollupBlocksTx(ctx, tx, item.id, resolution, kept); err != nil {
			return err
		}
	}
	return nil
}
