package metric

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

// incrementalCompactionPending avoids entering SQLite's write path when a
// finite-retention metric has no work. Other drivers retain the established
// transaction behavior because their physical layouts and concurrency models
// differ from SQLite V4.
func (s *Store) incrementalCompactionPending(ctx context.Context, metricName string, now time.Time, policy RollupPolicy, obsoleteIntervals []time.Duration) (bool, error) {
	if !s.sqliteStorageV4 || policy.RawRetention <= 0 {
		return true, nil
	}

	rawCutoff := policy.rawCutoff(now).UnixNano()
	pending, err := s.sqliteV4MetricPointRowsBefore(ctx, metricName, rawCutoff, true)
	if err != nil || pending {
		return pending, err
	}

	for _, interval := range obsoleteIntervals {
		pending, err = s.sqliteV4MetricRollupRows(ctx, metricName, interval.Nanoseconds(), nil)
		if err != nil || pending {
			return pending, err
		}
	}

	for index, tier := range policy.Tiers {
		alignment := tier.Interval
		if index+1 < len(policy.Tiers) {
			alignment = policy.Tiers[index+1].Interval
		}
		cutoff := alignRollupRetentionCutoff(now.Add(-tier.Retention), alignment).UnixNano()
		pending, err = s.sqliteV4MetricRollupRows(ctx, metricName, tier.Interval.Nanoseconds(), &cutoff)
		if err != nil || pending {
			return pending, err
		}
	}

	sealBefore := now.Add(-sqliteV4HotWindow).UnixNano()
	pending, err = s.sqliteV4MetricPointRowsBefore(ctx, metricName, sealBefore, false)
	if err != nil || pending {
		return pending, err
	}
	pending, err = s.sqliteV4MetricRollupSealPending(ctx, metricName, sealBefore)
	if err != nil || pending {
		return pending, err
	}
	return s.sqliteV4MetricHasUnusedSeries(ctx, metricName)
}

func (s *Store) sqliteV4MetricPointRowsBefore(ctx context.Context, metricName string, beforeNano int64, includeBlocks bool) (bool, error) {
	pending, err := queryRowExists(ctx, s.db, fmt.Sprintf(
		`SELECT 1 FROM %s p JOIN %s s ON s.id = p.series_id
		 WHERE s.metric_name = ? AND p.ts_nano < ? LIMIT 1`,
		s.tables.pointValues, s.tables.series,
	), metricName, beforeNano)
	if err != nil || pending || !includeBlocks {
		return pending, err
	}
	return queryRowExists(ctx, s.db, fmt.Sprintf(
		`SELECT 1 FROM %s b JOIN %s s ON s.id = b.series_id
		 WHERE s.metric_name = ? AND b.start_nano < ? LIMIT 1`,
		s.tables.pointBlocks, s.tables.series,
	), metricName, beforeNano)
}

func (s *Store) sqliteV4MetricRollupRows(ctx context.Context, metricName string, resolution int64, beforeNano *int64) (bool, error) {
	hotWhere := ""
	blockWhere := ""
	args := []any{metricName, resolution}
	if beforeNano != nil {
		hotWhere = " AND r.bucket_nano < ?"
		blockWhere = " AND b.start_nano < ?"
		args = append(args, *beforeNano)
	}
	pending, err := queryRowExists(ctx, s.db, fmt.Sprintf(
		`SELECT 1 FROM %s r JOIN %s s ON s.id = r.series_id
		 WHERE s.metric_name = ? AND r.resolution_nano = ?%s LIMIT 1`,
		s.tables.rollupValues, s.tables.series, hotWhere,
	), args...)
	if err != nil || pending {
		return pending, err
	}
	return queryRowExists(ctx, s.db, fmt.Sprintf(
		`SELECT 1 FROM %s b JOIN %s s ON s.id = b.series_id
		 WHERE s.metric_name = ? AND b.resolution_nano = ?%s LIMIT 1`,
		s.tables.rollupBlocks, s.tables.series, blockWhere,
	), args...)
}

func (s *Store) sqliteV4MetricRollupSealPending(ctx context.Context, metricName string, beforeNano int64) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT DISTINCT r.series_id, r.resolution_nano FROM %s r
		 JOIN %s s ON s.id = r.series_id
		 WHERE s.metric_name = ? AND r.bucket_nano < ?`,
		s.tables.rollupValues, s.tables.series,
	), metricName, beforeNano)
	if err != nil {
		return false, err
	}
	type rollupGroup struct {
		seriesID   int64
		resolution int64
	}
	var groups []rollupGroup
	for rows.Next() {
		var group rollupGroup
		if err := rows.Scan(&group.seriesID, &group.resolution); err != nil {
			_ = rows.Close()
			return false, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}

	for _, group := range groups {
		var maxEnd sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT MAX(end_nano) FROM `+s.tables.rollupBlocks+` WHERE series_id = ? AND resolution_nano = ?`, group.seriesID, group.resolution).Scan(&maxEnd); err != nil {
			return false, err
		}
		if maxEnd.Valid {
			lateBeforeNano := beforeNano - (sqliteV4RollupFlushWindow - sqliteV4HotWindow).Nanoseconds() - group.resolution
			pending, err := queryRowExists(ctx, s.db, `SELECT 1 FROM `+s.tables.rollupValues+` WHERE series_id = ? AND resolution_nano = ? AND bucket_nano <= ? AND bucket_nano <= ? LIMIT 1`, group.seriesID, group.resolution, maxEnd.Int64, lateBeforeNano)
			if err != nil || pending {
				return pending, err
			}
		}

		lower := int64(math.MinInt64)
		if maxEnd.Valid {
			lower = maxEnd.Int64
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+s.tables.rollupValues+` WHERE series_id = ? AND resolution_nano = ? AND bucket_nano > ? AND bucket_nano < ?`, group.seriesID, group.resolution, lower, beforeNano).Scan(&count); err != nil {
			return false, err
		}
		if count >= sqliteV4RollupFlushMinimum(group.resolution) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) sqliteV4MetricHasUnusedSeries(ctx context.Context, metricName string) (bool, error) {
	return queryRowExists(ctx, s.db, fmt.Sprintf(
		`SELECT 1 FROM %s s WHERE s.metric_name = ?
		 AND NOT EXISTS (SELECT 1 FROM %s p WHERE p.series_id = s.id)
		 AND NOT EXISTS (SELECT 1 FROM %s b WHERE b.series_id = s.id)
		 AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.series_id = s.id)
		 AND NOT EXISTS (SELECT 1 FROM %s b WHERE b.series_id = s.id)
		 LIMIT 1`,
		s.tables.series, s.tables.pointValues, s.tables.pointBlocks,
		s.tables.rollupValues, s.tables.rollupBlocks,
	), metricName)
}

func queryRowExists(ctx context.Context, q querier, query string, args ...any) (bool, error) {
	var marker int
	err := q.QueryRowContext(ctx, query, args...).Scan(&marker)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}
