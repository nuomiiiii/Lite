package metric

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
)

const sqliteStorageVersionPingMerge = 8

func (s *Store) migrateSQLiteV8PingSeries(ctx context.Context) (int64, error) {
	var userVersion int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return 0, fmt.Errorf("metric: inspect SQLite V8 migration version: %w", err)
	}
	if userVersion >= sqliteStorageVersionPingMerge {
		s.sqlitePingMerged = true
		return 0, nil
	}

	latencySeries, err := s.sqliteV4MatchingSeries(ctx, s.db, sqliteMergedPingLatencyMetric, "", nil)
	if err != nil {
		return 0, err
	}
	lossSeries, err := s.sqliteV4MatchingSeries(ctx, s.db, sqliteVirtualPingLossMetric, "", nil)
	if err != nil {
		return 0, err
	}
	lossByIdentity := make(map[string]sqliteV4Series, len(lossSeries))
	for _, series := range lossSeries {
		lossByIdentity[sqliteV8SeriesIdentity(series)] = series
	}
	latencyByIdentity := make(map[string]struct{}, len(latencySeries))
	for _, series := range latencySeries {
		latencyByIdentity[sqliteV8SeriesIdentity(series)] = struct{}{}
	}
	for identity := range lossByIdentity {
		if _, ok := latencyByIdentity[identity]; !ok {
			return 0, fmt.Errorf("metric: cannot merge ping loss series %q without matching latency data", identity)
		}
	}

	s.reportMigrationProgress(MigrationPhaseMergingPingSeries, 0, int64(len(latencySeries)), 0)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var migrated int64
	for index, latency := range latencySeries {
		loss, paired := lossByIdentity[sqliteV8SeriesIdentity(latency)]
		latencyPoints, err := s.loadSQLiteV8SeriesPointsTx(ctx, tx, latency.id)
		if err != nil {
			return migrated, err
		}
		if paired {
			lossPoints, err := s.loadSQLiteV8SeriesPointsTx(ctx, tx, loss.id)
			if err != nil {
				return migrated, err
			}
			if err := validateSQLiteV8PingPoints(latency, latencyPoints, lossPoints); err != nil {
				return migrated, err
			}
		}

		latencyRollups, err := s.loadSQLiteV8RollupsTx(ctx, tx, latency.id)
		if err != nil {
			return migrated, err
		}
		var lossRollups map[int64]map[int64]sqliteV4RollupRecord
		if paired {
			lossRollups, err = s.loadSQLiteV8RollupsTx(ctx, tx, loss.id)
			if err != nil {
				return migrated, err
			}
			if len(lossRollups) != len(latencyRollups) {
				return migrated, fmt.Errorf("metric: ping rollup resolution count mismatch series=%q latency=%d loss=%d", latency.entityID, len(latencyRollups), len(lossRollups))
			}
		}
		for resolution, buckets := range latencyRollups {
			pairedBuckets := lossRollups[resolution]
			if paired && len(pairedBuckets) != len(buckets) {
				return migrated, fmt.Errorf("metric: ping rollup bucket count mismatch series=%q resolution=%d latency=%d loss=%d", latency.entityID, resolution, len(buckets), len(pairedBuckets))
			}
			converted := make([]sqliteV4RollupRecord, 0, len(buckets))
			for bucketNano, record := range buckets {
				lossRecord, hasLossRecord := pairedBuckets[bucketNano]
				if paired && !hasLossRecord {
					return migrated, fmt.Errorf("metric: ping loss rollup bucket missing series=%q resolution=%d bucket=%d", latency.entityID, resolution, bucketNano)
				}
				lossCount, err := sqliteV8LossCount(record, lossRecord, paired)
				if err != nil {
					return migrated, fmt.Errorf("metric: merge ping rollup series=%q resolution=%d bucket=%d: %w", latency.entityID, resolution, bucketNano, err)
				}
				record, err = convertSQLiteV8LatencyRollup(record, lossCount)
				if err != nil {
					return migrated, fmt.Errorf("metric: convert ping rollup series=%q resolution=%d bucket=%d: %w", latency.entityID, resolution, bucketNano, err)
				}
				converted = append(converted, record)
				migrated++
			}
			sort.Slice(converted, func(i, j int) bool { return converted[i].bucketNano < converted[j].bucketNano })
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.tables.rollupBlocks+` WHERE series_id = ? AND resolution_nano = ?`, latency.id, resolution); err != nil {
				return migrated, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.tables.rollupValues+` WHERE series_id = ? AND resolution_nano = ?`, latency.id, resolution); err != nil {
				return migrated, err
			}
			if err := s.writeSQLiteV4RollupBlocksTx(ctx, tx, latency.id, resolution, converted); err != nil {
				return migrated, err
			}
		}
		s.reportMigrationProgress(MigrationPhaseMergingPingSeries, int64(index+1), int64(len(latencySeries)), migrated)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.tables.series+` WHERE metric_name = ?`, sqliteVirtualPingLossMetric); err != nil {
		return migrated, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.tables.watermarks+` WHERE metric_name = ?`, sqliteVirtualPingLossMetric); err != nil {
		return migrated, err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sqliteStorageVersionPingMerge)); err != nil {
		return migrated, err
	}
	if err := tx.Commit(); err != nil {
		return migrated, fmt.Errorf("metric: commit SQLite V8 ping merge: %w", err)
	}
	s.sqlitePingMerged = true
	return migrated, nil
}

func sqliteV8SeriesIdentity(series sqliteV4Series) string {
	return series.entityID + "\x00" + series.tagsHash
}

func (s *Store) loadSQLiteV8SeriesPointsTx(ctx context.Context, tx *sql.Tx, seriesID int64) (map[int64]sqliteV4BlockPoint, error) {
	blocks, err := s.loadAllSQLiteV4BlockPoints(ctx, tx, seriesID)
	if err != nil {
		return nil, err
	}
	points := make(map[int64]sqliteV4BlockPoint, len(blocks))
	for _, point := range blocks {
		points[point.timestamp] = point
	}
	rows, err := tx.QueryContext(ctx, `SELECT ts_nano, value, labels, created_at FROM `+s.tables.pointValues+` WHERE series_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var point sqliteV4BlockPoint
		var value float64
		var labels any
		if err := rows.Scan(&point.timestamp, &value, &labels, &point.createdAt); err != nil {
			return nil, err
		}
		point.valueBits = math.Float64bits(value)
		point.labels, err = rawTagsToJSON(labels)
		if err != nil {
			return nil, err
		}
		points[point.timestamp] = point
	}
	return points, rows.Err()
}

func validateSQLiteV8PingPoints(series sqliteV4Series, latency, loss map[int64]sqliteV4BlockPoint) error {
	if len(latency) != len(loss) {
		return fmt.Errorf("metric: ping point count mismatch series=%q latency=%d loss=%d", series.entityID, len(latency), len(loss))
	}
	for timestamp, latencyPoint := range latency {
		lossPoint, ok := loss[timestamp]
		if !ok {
			return fmt.Errorf("metric: ping loss point missing series=%q timestamp=%d", series.entityID, timestamp)
		}
		want := 0.0
		if math.Float64frombits(latencyPoint.valueBits) < 0 {
			want = 1
		}
		if math.Float64bits(want) != lossPoint.valueBits {
			return fmt.Errorf("metric: ping loss point mismatch series=%q timestamp=%d", series.entityID, timestamp)
		}
	}
	return nil
}

func (s *Store) loadSQLiteV8RollupsTx(ctx context.Context, tx *sql.Tx, seriesID int64) (map[int64]map[int64]sqliteV4RollupRecord, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		`SELECT resolution_nano FROM %s WHERE series_id = ? UNION SELECT resolution_nano FROM %s WHERE series_id = ?`,
		s.tables.rollupValues, s.tables.rollupBlocks,
	), seriesID, seriesID)
	if err != nil {
		return nil, err
	}
	var resolutions []int64
	for rows.Next() {
		var resolution int64
		if err := rows.Scan(&resolution); err != nil {
			_ = rows.Close()
			return nil, err
		}
		resolutions = append(resolutions, resolution)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make(map[int64]map[int64]sqliteV4RollupRecord, len(resolutions))
	for _, resolution := range resolutions {
		records, err := s.loadAllSQLiteV4RollupBlockRecords(ctx, tx, seriesID, resolution)
		if err != nil {
			return nil, err
		}
		byBucket := make(map[int64]sqliteV4RollupRecord, len(records))
		for _, record := range records {
			byBucket[record.bucketNano] = record
		}
		hot, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT bucket_nano, count, loss_count, sum, sum_sq, min_val, max_val, first_val, first_ts,
			        last_val, last_ts, digest, created_at FROM %s WHERE series_id = ? AND resolution_nano = ?`,
			s.tables.rollupValues,
		), seriesID, resolution)
		if err != nil {
			return nil, err
		}
		for hot.Next() {
			record, err := scanSQLiteV4RollupRecord(hot)
			if err != nil {
				_ = hot.Close()
				return nil, err
			}
			byBucket[record.bucketNano] = record
		}
		if err := hot.Close(); err != nil {
			return nil, err
		}
		result[resolution] = byBucket
	}
	return result, nil
}

func sqliteV8LossCount(latency, loss sqliteV4RollupRecord, paired bool) (int64, error) {
	if !paired {
		if len(latency.digest) == 0 {
			return 0, nil
		}
		digest, err := DecodeTDigest(latency.digest)
		if err != nil {
			return 0, err
		}
		digest.process()
		var count float64
		for _, centroid := range digest.centroids {
			if centroid.mean < 0 {
				count += centroid.weight
			}
		}
		if count < 0 || count > float64(latency.count) || math.Trunc(count) != count {
			return 0, fmt.Errorf("cannot derive an exact loss count from the latency digest")
		}
		return int64(count), nil
	}
	if loss.bucketNano != latency.bucketNano || loss.count != latency.count {
		return 0, fmt.Errorf("latency/loss summaries do not describe the same samples")
	}
	lossSum := math.Float64frombits(loss.sumBits)
	if math.IsNaN(lossSum) || math.IsInf(lossSum, 0) || math.Trunc(lossSum) != lossSum || lossSum < 0 || lossSum > float64(loss.count) {
		return 0, fmt.Errorf("loss summary is not an exact 0/1 count")
	}
	lossCount := int64(lossSum)
	if math.Float64frombits(loss.sumSqBits) != lossSum {
		return 0, fmt.Errorf("loss summary square count is inconsistent")
	}
	return lossCount, nil
}

func convertSQLiteV8LatencyRollup(record sqliteV4RollupRecord, lossCount int64) (sqliteV4RollupRecord, error) {
	if lossCount < 0 || lossCount > record.count {
		return sqliteV4RollupRecord{}, fmt.Errorf("invalid loss count %d/%d", lossCount, record.count)
	}
	record.lossCount = lossCount
	if lossCount == 0 {
		return record, nil
	}
	record.sumBits = math.Float64bits(math.Float64frombits(record.sumBits) + float64(lossCount))
	record.sumSqBits = math.Float64bits(math.Float64frombits(record.sumSqBits) - float64(lossCount))
	if lossCount == record.count {
		record.sumBits = math.Float64bits(0)
		record.sumSqBits = math.Float64bits(0)
		record.minBits = math.Float64bits(0)
		record.maxBits = math.Float64bits(0)
		record.digest = nil
		return record, nil
	}
	if len(record.digest) == 0 {
		// This is an already-unrecoverable historical digest. Preserve its
		// readable scalar summary and do not turn a prior loss into a startup
		// failure; all newly written buckets use the exact V8 representation.
		return record, nil
	}
	digest, err := DecodeTDigest(record.digest)
	if err != nil {
		return sqliteV4RollupRecord{}, err
	}
	digest.process()
	positive := NewTDigest(digest.compression)
	var removed float64
	for _, centroid := range digest.centroids {
		if centroid.mean < 0 {
			if centroid.mean != -1 {
				return sqliteV4RollupRecord{}, fmt.Errorf("negative latency digest contains a non-sentinel mean %v", centroid.mean)
			}
			removed += centroid.weight
			continue
		}
		positive.centroids = append(positive.centroids, centroid)
		positive.count += centroid.weight
	}
	if removed != float64(lossCount) {
		return sqliteV4RollupRecord{}, fmt.Errorf("latency digest loss weight=%v want=%d", removed, lossCount)
	}
	if len(positive.centroids) == 0 {
		return sqliteV4RollupRecord{}, fmt.Errorf("latency digest lost all valid centroids")
	}
	positive.min = positive.centroids[0].mean
	positive.max = positive.centroids[len(positive.centroids)-1].mean
	positive.processed = true
	record.minBits = math.Float64bits(positive.min)
	record.maxBits = math.Float64bits(positive.max)
	record.digest = positive.Encode()
	return record, nil
}

func (s *Store) ensureSQLiteV8PingColumns(ctx context.Context) error {
	columns, err := sqliteColumns(ctx, s.db, s.tables.rollupValues)
	if err != nil {
		return fmt.Errorf("metric: inspect SQLite V8 rollup columns: %w", err)
	}
	if columns["loss_count"] {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE `+s.tables.rollupValues+` ADD COLUMN loss_count BIGINT NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("metric: add SQLite V8 loss count: %w", err)
	}
	return nil
}

func (s *Store) upsertSQLiteV8RollupValueTx(
	ctx context.Context,
	tx *sql.Tx,
	metricName string,
	key rollupKey,
	tagsJSON string,
	resolution int64,
	bucket *rollupBucket,
	createdAt int64,
) error {
	if bucket.lossCount < 0 || bucket.lossCount > bucket.count {
		return fmt.Errorf("metric: invalid ping loss count %d/%d", bucket.lossCount, bucket.count)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (metric_name, entity_id, tags_hash, tags) VALUES (?, ?, ?, ?)
		 ON CONFLICT(metric_name, entity_id, tags_hash) DO UPDATE SET tags = excluded.tags`, s.tables.series,
	), metricName, key.entityID, key.tagsHash, tagsJSON); err != nil {
		return err
	}
	var seriesID int64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT id FROM %s WHERE metric_name = ? AND entity_id = ? AND tags_hash = ?`, s.tables.series,
	), metricName, key.entityID, key.tagsHash).Scan(&seriesID); err != nil {
		return err
	}
	digest := []byte(nil)
	if bucket.digest != nil {
		digest = bucket.digest.Encode()
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s
		 (series_id, resolution_nano, bucket_nano, count, loss_count, sum, sum_sq, min_val, max_val,
		  first_val, first_ts, last_val, last_ts, digest, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(series_id, resolution_nano, bucket_nano) DO UPDATE SET
		  count = excluded.count, loss_count = excluded.loss_count,
		  sum = excluded.sum, sum_sq = excluded.sum_sq,
		  min_val = excluded.min_val, max_val = excluded.max_val,
		  first_val = excluded.first_val, first_ts = excluded.first_ts,
		  last_val = excluded.last_val, last_ts = excluded.last_ts,
		  digest = excluded.digest, created_at = excluded.created_at`, s.tables.rollupValues,
	), seriesID, resolution, key.bucket, bucket.count, bucket.lossCount,
		bucket.sum, bucket.sumSq, bucket.min, bucket.max,
		bucket.firstVal, bucket.firstTS, bucket.lastVal, bucket.lastTS, digest, createdAt)
	return err
}
