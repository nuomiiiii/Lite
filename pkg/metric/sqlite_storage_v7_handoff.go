package metric

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

const sqliteStorageVersionTierHandoff = 7

// migrateSQLiteV7TierHandoff converts the former overlapping ladder to true
// handoff ranges. Existing coarse summaries are validated against their finer
// source before either copy is removed. Missing coarse digests are rebuilt when
// the complete finer digest still exists; already-unrecoverable historical
// digests remain readable as ordinary summaries and do not block startup.
func (s *Store) migrateSQLiteV7TierHandoff(ctx context.Context, now time.Time) (int64, error) {
	var userVersion int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return 0, fmt.Errorf("metric: inspect tier-handoff migration version: %w", err)
	}
	if userVersion >= sqliteStorageVersionTierHandoff {
		return 0, nil
	}
	definitions, err := s.ListMetrics(ctx)
	if err != nil {
		return 0, err
	}
	s.reportMigrationProgress(MigrationPhaseHandoffTiers, 0, int64(len(definitions)), 0)
	var migrated int64
	for index, definition := range definitions {
		policy := s.cfg.RollupPolicy.withMetricRetention(time.Duration(definition.RetentionDays) * 24 * time.Hour)
		if definition.RetentionDays <= 0 || len(policy.Tiers) == 0 {
			s.reportMigrationProgress(MigrationPhaseHandoffTiers, int64(index+1), int64(len(definitions)), migrated)
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return migrated, err
		}
		changed, migrateErr := s.migrateSQLiteV7MetricTiersTx(ctx, tx, definition.Name, now.UTC(), policy)
		if migrateErr != nil {
			_ = tx.Rollback()
			return migrated, fmt.Errorf("metric: migrate tier handoff for %q: %w", definition.Name, migrateErr)
		}
		if err := tx.Commit(); err != nil {
			return migrated, fmt.Errorf("metric: commit tier handoff for %q: %w", definition.Name, err)
		}
		migrated += changed
		s.reportMigrationProgress(MigrationPhaseHandoffTiers, int64(index+1), int64(len(definitions)), migrated)
	}
	return migrated, nil
}

func (s *Store) migrateSQLiteV7MetricTiersTx(ctx context.Context, tx *sql.Tx, metricName string, now time.Time, policy RollupPolicy) (int64, error) {
	var changed int64
	for index := 0; index+1 < len(policy.Tiers); index++ {
		fine := policy.Tiers[index]
		coarse := policy.Tiers[index+1]
		boundary := alignRollupRetentionCutoff(now.Add(-fine.Retention), coarse.Interval)
		upper := boundary.UnixNano()
		if upper == math.MinInt64 {
			continue
		}
		fineRows, err := s.scanRollupRowsRange(ctx, tx, metricName, fine.Interval, math.MinInt64, upper-1, true)
		if err != nil {
			return changed, err
		}
		coarseRows, err := s.scanRollupRowsRange(ctx, tx, metricName, coarse.Interval, math.MinInt64, upper-1, true)
		if err != nil {
			return changed, err
		}
		existing := make(map[rollupKey]*rollupBucket, len(coarseRows))
		for _, row := range coarseRows {
			existing[rollupKey{entityID: row.entityID, tagsHash: row.bucketData.tagsHash, bucket: row.bucket}] = row.bucketData
		}
		groups, missingDigest := coarsenSQLiteV7MigrationRows(metricName, fineRows, coarse.Interval, policy.compression())
		toWrite := make(map[rollupKey]*rollupBucket)
		for key, rebuilt := range groups {
			stored := existing[key]
			if stored == nil {
				if missingDigest[key] {
					return changed, fmt.Errorf("cannot create coarse bucket %d from a finer source with a missing digest", key.bucket)
				}
				toWrite[key] = rebuilt
				continue
			}
			if !sqliteV4RollupSummariesEqual(rebuilt, stored) {
				return changed, fmt.Errorf("tier summary mismatch at resolution=%s bucket=%d", coarse.Interval, key.bucket)
			}
			if stored.digest == nil && !missingDigest[key] && rebuilt.digest != nil {
				stored.digest = rebuilt.digest
				toWrite[key] = stored
			}
		}
		if len(toWrite) > 0 {
			n, err := s.writeRollupBucketsTx(ctx, metricName, coarse.Interval, toWrite, tx)
			if err != nil {
				return changed, err
			}
			changed += int64(n)
		}
		if err := s.rewriteSQLiteV4RollupRangeTx(ctx, tx, metricName, fine.Interval.Nanoseconds(), upper, math.MaxInt64); err != nil {
			return changed, err
		}
		if err := s.rewriteSQLiteV4RollupRangeTx(ctx, tx, metricName, coarse.Interval.Nanoseconds(), math.MinInt64, upper-1); err != nil {
			return changed, err
		}
		changed += int64(len(fineRows))
	}
	return changed, nil
}

func coarsenSQLiteV7MigrationRows(metricName string, rows []storedRollup, interval time.Duration, comp float64) (map[rollupKey]*rollupBucket, map[rollupKey]bool) {
	groups := make(map[rollupKey]*rollupBucket)
	missingDigest := make(map[rollupKey]bool)
	coarseNano := interval.Nanoseconds()
	for _, row := range rows {
		key := rollupKey{entityID: row.entityID, tagsHash: row.bucketData.tagsHash, bucket: floorDivNano(row.bucket, coarseNano)}
		bucket := groups[key]
		if bucket == nil {
			bucket = newRollupBucketWithDigest(comp, false)
			bucket.tagsHash = row.bucketData.tagsHash
			bucket.tagsJSON = row.bucketData.tagsJSON
			groups[key] = bucket
		}
		if row.bucketData.digest == nil && !rollupDigestOptional(metricName, row.bucketData) {
			missingDigest[key] = true
		}
		bucket.mergeStored(row.bucketData)
	}
	for _, bucket := range groups {
		if rollupDigestOptional(metricName, bucket) {
			bucket.digest = nil
		}
	}
	return groups, missingDigest
}
