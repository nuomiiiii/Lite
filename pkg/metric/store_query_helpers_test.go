package metric

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// DeleteMetricData removes all raw, rollup, and watermark data for one metric.
func (s *Store) DeleteMetricData(ctx context.Context, name string) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: metric name is required", ErrInvalidArgument)
	}

	s.retentionMu.Lock()
	defer s.retentionMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if s.sqliteStorageV4 {
		if _, err := s.deleteSQLiteV4PointsTx(ctx, tx, Query{MetricName: name}, nil); err != nil {
			return err
		}
		if _, err := s.deleteSQLiteV4RollupsTx(ctx, tx, Query{MetricName: name}, nil, nil); err != nil {
			return err
		}
	}
	tables := []string{s.tables.watermarks}
	if !s.sqliteStorageV4 {
		tables = append(tables, s.tables.points, s.tables.rollups)
	}
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE metric_name = %s`, table, s.dialect.placeholder(1)), name,
		); err != nil {
			return err
		}
	}
	if err := s.pruneUnusedSQLiteSeries(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Latest loads the newest points for a metric and entity.
//
// Latest 查询某指标和实体的最新采样点。
func (s *Store) Latest(ctx context.Context, metricName, entityID string, limit int) ([]Point, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(metricName) == "" {
		return nil, fmt.Errorf("%w: metric name is required", ErrInvalidArgument)
	}
	if strings.TrimSpace(entityID) == "" {
		return nil, fmt.Errorf("%w: entity id is required", ErrInvalidArgument)
	}
	if limit <= 0 {
		limit = 1
	}
	virtualLoss := s.sqlitePingMerged && metricName == sqliteVirtualPingLossMetric
	if virtualLoss {
		metricName = sqliteMergedPingLatencyMetric
	}
	if s.sqliteStorageV4 {
		points, err := s.querySQLiteV4Snapshot(ctx, Query{
			MetricName: metricName,
			EntityID:   entityID,
			Start:      time.Unix(0, math.MinInt64).UTC(),
			End:        time.Unix(0, math.MaxInt64).UTC(),
			Order:      OrderDesc,
			Limit:      limit,
		})
		if err != nil || !virtualLoss {
			return points, err
		}
		return restoreVirtualPingLossPoints(points), nil
	}
	// Dedicated query rather than a full-range Query: no synthetic time bounds,
	// and the index on (metric_name, entity_id, ts_nano) serves the ORDER BY.
	sqlText := fmt.Sprintf(
		`SELECT metric_name, entity_id, ts_nano, value, tags, labels FROM %s WHERE metric_name = %s AND entity_id = %s ORDER BY ts_nano DESC LIMIT %s`,
		s.tables.points, s.dialect.placeholder(1), s.dialect.placeholder(2), s.dialect.placeholder(3),
	)
	rows, err := s.reader().QueryContext(ctx, sqlText, metricName, entityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Point
	for rows.Next() {
		var p Point
		var ts int64
		var rawTags, rawLabels any
		if err := rows.Scan(&p.MetricName, &p.EntityID, &ts, &p.Value, &rawTags, &rawLabels); err != nil {
			return nil, err
		}
		p.Timestamp = time.Unix(0, ts).UTC()
		p.Tags, err = decodeMap(rawTags)
		if err != nil {
			return nil, err
		}
		p.Labels, err = decodeMap(rawLabels)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if virtualLoss {
		return restoreVirtualPingLossPoints(out), nil
	}
	return out, nil
}

// Stats stores or computes summary statistics for a point series.
//
// Stats 查询原始点并计算统计摘要。
func (s *Store) Stats(ctx context.Context, query Query) (Stats, error) {
	points, err := s.Query(ctx, query)
	if err != nil {
		return Stats{}, err
	}
	var stats Stats
	if s.sqlitePingMerged && query.MetricName == sqliteMergedPingLatencyMetric {
		stats, err = calculateMergedPingLatencyStats(points)
	} else {
		stats, err = CalculateStats(points)
	}
	if errors.Is(err, ErrNoData) {
		// No samples in range. Disambiguate from a non-existent metric so the
		// caller can tell "empty window" apart from "unknown metric".
		if _, gerr := s.GetMetric(ctx, query.MetricName); errors.Is(gerr, ErrNotFound) {
			return Stats{}, ErrNotFound
		} else if gerr != nil {
			return Stats{}, gerr
		}
	}
	return stats, err
}
