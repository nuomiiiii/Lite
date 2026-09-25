package metric

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

func (s *Store) migrateSQLiteV4RedundantRollupDigests(ctx context.Context, now time.Time, force bool) (int64, int64, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT name, retention_days FROM %s ORDER BY name`, s.tables.definitions))
	if err != nil {
		return 0, 0, fmt.Errorf("metric: list definitions for digest handoff migration: %w", err)
	}
	type metricRetention struct {
		name string
		days int
	}
	var metrics []metricRetention
	for rows.Next() {
		var item metricRetention
		if err := rows.Scan(&item.name, &item.days); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		metrics = append(metrics, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}
	if len(metrics) == 0 {
		return 0, 0, nil
	}

	s.reportMigrationProgress(MigrationPhaseUpgradingRollupBlocks, 0, int64(len(metrics)), 0)
	var rewrittenBlocks, rewrittenBuckets, deferredMetrics int64
	for index, item := range metrics {
		if item.days > 0 {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: begin digest handoff migration for %q: %w", item.name, err)
			}
			policy := s.cfg.RollupPolicy.withMetricRetention(time.Duration(item.days) * 24 * time.Hour)
			blocks, buckets, syncErr := s.syncSQLiteV4RedundantRollupDigestsTx(ctx, tx, item.name, now.UTC(), policy, force)
			if syncErr != nil {
				_ = tx.Rollback()
				if errors.Is(syncErr, errSQLiteV4DigestHandoffDeferred) {
					// Force the runtime retry to scan again before retention advances.
					if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE metric_name = ?`, s.tables.watermarks), item.name); err != nil {
						return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: reset compaction watermark for deferred digest handoff %q: %w", item.name, err)
					}
					deferredMetrics++
					log.Printf("metric: preserving %q digest blocks and deferring lossless handoff: %v", item.name, syncErr)
					s.reportMigrationProgressWithDeferred(MigrationPhaseUpgradingRollupBlocks, int64(index+1), int64(len(metrics)), rewrittenBuckets, deferredMetrics)
					continue
				}
				return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: migrate digest handoff for %q: %w", item.name, syncErr)
			}
			if err := tx.Commit(); err != nil {
				return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: commit digest handoff migration for %q: %w", item.name, err)
			}
			rewrittenBlocks += blocks
			rewrittenBuckets += buckets
		}
		s.reportMigrationProgress(MigrationPhaseUpgradingRollupBlocks, int64(index+1), int64(len(metrics)), rewrittenBuckets)
	}
	if deferredMetrics > 0 {
		log.Printf("metric: digest handoff deferred for %d metric definitions; existing digest data was preserved", deferredMetrics)
	}
	return rewrittenBlocks, rewrittenBuckets, nil
}

func sqliteV4CeilNano(value, step int64) (int64, error) {
	if step <= 0 {
		return value, nil
	}
	floor := floorDivNano(value, step)
	if floor == value {
		return value, nil
	}
	return checkedAddInt64(floor, step)
}

func (s *Store) syncSQLiteV4RedundantRollupDigestsTx(ctx context.Context, tx *sql.Tx, metricName string, now time.Time, policy RollupPolicy, force bool) (int64, int64, error) {
	if len(policy.Tiers) < 2 || policy.RawRetention <= 0 {
		return 0, 0, nil
	}
	fine := policy.Tiers[0]
	coarse := policy.Tiers[1]
	if coarse.Retention <= fine.Retention || coarse.Interval%fine.Interval != 0 {
		return 0, 0, nil
	}
	step := sqliteV4RollupDigestHandoffWindow
	if coarse.Interval > step || step%coarse.Interval != 0 {
		step = coarse.Interval
	}
	digestCutoff, err := sqliteV4CeilNano(now.UTC().Add(-fine.Retention).UnixNano(), step.Nanoseconds())
	if err != nil {
		return 0, 0, err
	}
	if !force {
		var previousRawCutoff int64
		err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT watermark_nano FROM %s WHERE metric_name = ?`, s.tables.watermarks), metricName).Scan(&previousRawCutoff)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, 0, err
		}
		if err == nil && previousRawCutoff > 0 {
			previousNow, addErr := checkedAddInt64(previousRawCutoff, policy.RawRetention.Nanoseconds())
			if addErr != nil {
				return 0, 0, addErr
			}
			previousDigestCutoff, ceilErr := sqliteV4CeilNano(previousNow-fine.Retention.Nanoseconds(), step.Nanoseconds())
			if ceilErr != nil {
				return 0, 0, ceilErr
			}
			if previousDigestCutoff == digestCutoff {
				return 0, 0, nil
			}
		}
	}
	series, err := s.sqliteV4MatchingSeries(ctx, tx, metricName, "", nil)
	if err != nil {
		return 0, 0, err
	}
	var rewrittenBlocks, rewrittenBuckets int64
	for _, item := range series {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT b.start_nano, b.end_nano, b.bucket_count, b.codec, b.checksum, b.payload,
			        b.digest_codec, b.digest_checksum, b.digest_payload, a.codec, a.checksum, a.payload
			 FROM %s AS b LEFT JOIN %s AS a ON a.id = b.axis_id
			 WHERE b.series_id = ? AND b.resolution_nano = ? ORDER BY b.start_nano`,
			s.tables.rollupBlocks, s.tables.rollupAxes,
		), item.id, coarse.Interval.Nanoseconds())
		if err != nil {
			return rewrittenBlocks, rewrittenBuckets, err
		}
		var blocks []sqliteV4RollupStoredBlock
		for rows.Next() {
			var block sqliteV4RollupStoredBlock
			var checksum, digestChecksum int64
			if err := rows.Scan(&block.startNano, &block.endNano, &block.count, &block.codec, &checksum, &block.payload,
				&block.digestCodec, &digestChecksum, &block.digestPayload, &block.axisCodec, &block.axisChecksum, &block.axisPayload); err != nil {
				_ = rows.Close()
				return rewrittenBlocks, rewrittenBuckets, err
			}
			block.checksum = uint32(checksum)
			block.digestChecksum = uint32(digestChecksum)
			blocks = append(blocks, block)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return rewrittenBlocks, rewrittenBuckets, err
		}
		if err := rows.Close(); err != nil {
			return rewrittenBlocks, rewrittenBuckets, err
		}
		for _, block := range blocks {
			records, err := decodeSQLiteV4RollupBlockWithAxisReference(
				block.codec, block.count, block.checksum, block.payload,
				block.axisCodec, block.axisChecksum, block.axisPayload,
				block.digestCodec, block.digestChecksum, block.digestPayload, true,
			)
			if err != nil {
				return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: decode digest handoff block series=%d start=%d: %w", item.id, block.startNano, err)
			}
			if len(records) == 0 || records[0].bucketNano != block.startNano || records[len(records)-1].bucketNano != block.endNano {
				return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: digest handoff block boundary mismatch series=%d start=%d", item.id, block.startNano)
			}
			changed := false
			var restore, dropDuplicate []int
			for index := range records {
				if records[index].bucketNano >= digestCutoff {
					if len(records[index].digest) > 0 {
						dropDuplicate = append(dropDuplicate, index)
					}
				} else if len(records[index].digest) == 0 {
					restore = append(restore, index)
				}
			}
			if len(restore) > 0 || len(dropDuplicate) > 0 {
				fineUpper, err := checkedAddInt64(block.endNano, coarse.Interval.Nanoseconds()-fine.Interval.Nanoseconds())
				if err != nil {
					return rewrittenBlocks, rewrittenBuckets, err
				}
				fineRecords, err := s.loadAllSQLiteV4RollupBlockRecords(ctx, tx, item.id, fine.Interval.Nanoseconds())
				if err != nil {
					return rewrittenBlocks, rewrittenBuckets, err
				}
				groups := make(map[int64]*rollupBucket)
				incomplete := make(map[int64]struct{})
				for _, fineRecord := range fineRecords {
					if fineRecord.bucketNano < block.startNano || fineRecord.bucketNano > fineUpper {
						continue
					}
					bucketNano := floorDivNano(fineRecord.bucketNano, coarse.Interval.Nanoseconds())
					bucketData, err := sqliteV4RollupBucketFromRecord(fineRecord, item, true)
					if err != nil {
						return rewrittenBlocks, rewrittenBuckets, err
					}
					if len(fineRecord.digest) == 0 && !rollupDigestOptional(item.metricName, bucketData) {
						incomplete[bucketNano] = struct{}{}
						continue
					}
					bucket := groups[bucketNano]
					if bucket == nil {
						bucket = newRollupBucketWithDigest(policy.compression(), false)
						groups[bucketNano] = bucket
					}
					bucket.mergeStored(bucketData)
				}
				for _, index := range restore {
					record := &records[index]
					if _, missing := incomplete[record.bucketNano]; missing {
						// This old finer digest is already absent and cannot reappear on a retry.
						continue
					}
					rebuilt := groups[record.bucketNano]
					stored, err := sqliteV4RollupBucketFromRecord(*record, item, true)
					if err != nil {
						return rewrittenBlocks, rewrittenBuckets, err
					}
					if rebuilt != nil && rollupDigestOptional(item.metricName, rebuilt) &&
						rollupDigestOptional(item.metricName, stored) {
						// There are no latency samples to sketch. Keeping the digest
						// absent is the exact, lossless representation for this bucket.
						continue
					}
					if rebuilt == nil || rebuilt.digest == nil || rebuilt.count != stored.count {
						// No complete finer source remains. Keep the readable coarse summary,
						// skip the unrecoverable digest, and continue with later buckets.
						continue
					}
					if !sqliteV4RollupSummariesEqual(rebuilt, stored) {
						return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("%w: cannot losslessly hand off digest series=%d bucket=%d", errSQLiteV4DigestHandoffDeferred, item.id, record.bucketNano)
					}
					record.digest = rebuilt.digest.Encode()
					changed = true
				}
				for _, index := range dropDuplicate {
					record := &records[index]
					if _, missing := incomplete[record.bucketNano]; missing {
						continue
					}
					rebuilt := groups[record.bucketNano]
					stored, err := sqliteV4RollupBucketFromRecord(*record, item, true)
					if err != nil {
						return rewrittenBlocks, rewrittenBuckets, err
					}
					if rebuilt != nil && rollupDigestOptional(item.metricName, rebuilt) &&
						rollupDigestOptional(item.metricName, stored) {
						record.digest = nil
						changed = true
						continue
					}
					if rebuilt == nil || rebuilt.digest == nil || !sqliteV4RollupSummariesEqual(rebuilt, stored) {
						continue
					}
					if !sqliteV4TDigestsEqual(record.digest, rebuilt.digest.Encode()) {
						continue
					}
					record.digest = nil
					changed = true
				}
			}
			if !changed {
				continue
			}
			encoded, err := encodeSQLiteV4RollupBlock(records)
			if err != nil {
				return rewrittenBlocks, rewrittenBuckets, err
			}
			decoded, err := decodeSQLiteV4StoredRollupBlock(
				encoded.codec, encoded.count, encoded.checksum, encoded.payload,
				encoded.axisCodec, encoded.axisChecksum, encoded.axisPayload,
				encoded.digestCodec, encoded.digestChecksum, encoded.digestPayload, true,
			)
			if err != nil || !sqliteV4RollupRecordDataSlicesEqual(records, decoded) {
				if err == nil {
					err = errors.New("digest handoff round-trip validation changed data")
				}
				return rewrittenBlocks, rewrittenBuckets, err
			}
			axisID, err := s.storeSQLiteV4RollupAxisTx(ctx, tx, encoded)
			if err != nil {
				return rewrittenBlocks, rewrittenBuckets, err
			}
			result, err := tx.ExecContext(ctx, fmt.Sprintf(
				`UPDATE %s SET end_nano = ?, bucket_count = ?, codec = ?, checksum = ?, payload = ?,
				        digest_codec = ?, digest_checksum = ?, digest_payload = ?, axis_id = ?
				 WHERE series_id = ? AND resolution_nano = ? AND start_nano = ?`, s.tables.rollupBlocks,
			), encoded.endNano, encoded.count, encoded.codec, int64(encoded.checksum), encoded.payload,
				encoded.digestCodec, int64(encoded.digestChecksum), encoded.digestPayload, axisID,
				item.id, coarse.Interval.Nanoseconds(), block.startNano)
			if err != nil {
				return rewrittenBlocks, rewrittenBuckets, err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: inspect digest handoff update: %w", err)
			}
			if affected != 1 {
				return rewrittenBlocks, rewrittenBuckets, fmt.Errorf("metric: digest handoff update affected %d rows, want 1", affected)
			}
			rewrittenBlocks++
			rewrittenBuckets += int64(len(records))
		}
	}
	return rewrittenBlocks, rewrittenBuckets, nil
}
