package metric

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

// DashboardSeries keeps entity/tag identities but skips the general Point
// reconstruction path for SQLite V4 raw data, including the raw tail of a
// mixed rollup query. Other storage layouts retain the exact Series behavior.
func (s *Store) DashboardSeries(ctx context.Context, query AggregateQuery, now time.Time) ([]AggregatePoint, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	if !s.sqliteStorageV4 || (query.Aggregation != AggAvg && query.Aggregation != AggSum && query.Aggregation != AggLast) {
		return s.Series(ctx, query, now)
	}
	virtualLoss := s.sqlitePingMerged && query.MetricName == sqliteVirtualPingLossMetric
	if virtualLoss {
		query.MetricName = sqliteMergedPingLatencyMetric
		query.Aggregation = pingLossPhysicalAggregation(query.Aggregation)
	}
	ctx = withDashboardAxisQueryCache(ctx)

	select {
	case s.heavyReadGate <- struct{}{}:
		defer func() { <-s.heavyReadGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	groups, err := s.collectSeriesPhysicalGroups(ctx, query, now, false)
	if err != nil {
		return nil, err
	}
	result, err := rollupGroupsToPoints(groups, query)
	if err != nil {
		return nil, err
	}
	if virtualLoss {
		result = restoreVirtualPingLossAggregates(result)
	}
	return pageBuckets(result, query.BucketLimit, query.BucketOffset), nil
}

func (s *Store) foldSQLiteV4RawDashboardSnapshot(ctx context.Context, query AggregateQuery, groups map[rollupKey]*rollupBucket) error {
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.foldSQLiteV4RawDashboard(ctx, tx, query, groups); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) foldSQLiteV4RawDashboard(ctx context.Context, q querier, query AggregateQuery, groups map[rollupKey]*rollupBucket) error {
	filter := query.Query.normalized()
	series, err := s.sqliteV4MatchingSeries(ctx, q, filter.MetricName, filter.EntityID, filter.Tags)
	if err != nil || len(series) == 0 {
		return err
	}
	seriesByID := make(map[int64]sqliteV4Series, len(series))
	for _, item := range series {
		seriesByID[item.id] = item
	}
	seriesWhere, seriesArgs := sqliteV4SeriesIDClause(series)
	startNano, endNano := filter.Start.UnixNano(), filter.End.UnixNano()

	// Hot values normally begin after the final immutable block. Detect the
	// exceptional overlap first, then retain exact hot keys only for affected
	// series so a rewritten hot value still wins without a fleet-sized key map.
	hotMinimum := make(map[int64]int64)
	hotArgs := append(append([]any{}, seriesArgs...), startNano, endNano)
	hotMinRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, MIN(ts_nano) FROM %s WHERE series_id IN (%s) AND ts_nano >= ? AND ts_nano <= ? GROUP BY series_id`,
		s.tables.pointValues, seriesWhere,
	), hotArgs...)
	if err != nil {
		return err
	}
	for hotMinRows.Next() {
		var seriesID, minimum int64
		if err := hotMinRows.Scan(&seriesID, &minimum); err != nil {
			_ = hotMinRows.Close()
			return err
		}
		hotMinimum[seriesID] = minimum
	}
	if err := hotMinRows.Err(); err != nil {
		_ = hotMinRows.Close()
		return err
	}
	if err := hotMinRows.Close(); err != nil {
		return err
	}

	overlappingSeries := make(map[int64]struct{})
	blockArgs := append(append([]any{}, seriesArgs...), startNano, endNano)
	blockMaxRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, MAX(end_nano) FROM %s WHERE series_id IN (%s) AND end_nano >= ? AND start_nano <= ? GROUP BY series_id`,
		s.tables.pointBlocks, seriesWhere,
	), blockArgs...)
	if err != nil {
		return err
	}
	for blockMaxRows.Next() {
		var seriesID, maximum int64
		if err := blockMaxRows.Scan(&seriesID, &maximum); err != nil {
			_ = blockMaxRows.Close()
			return err
		}
		if minimum, ok := hotMinimum[seriesID]; ok && maximum >= minimum {
			overlappingSeries[seriesID] = struct{}{}
		}
	}
	if err := blockMaxRows.Err(); err != nil {
		_ = blockMaxRows.Close()
		return err
	}
	if err := blockMaxRows.Close(); err != nil {
		return err
	}

	hotOverrides := make(map[sqliteV4PointKey]struct{})
	if len(overlappingSeries) > 0 {
		overlapSeries := make([]sqliteV4Series, 0, len(overlappingSeries))
		for seriesID := range overlappingSeries {
			overlapSeries = append(overlapSeries, seriesByID[seriesID])
		}
		overlapWhere, overlapArgs := sqliteV4SeriesIDClause(overlapSeries)
		overlapArgs = append(overlapArgs, startNano, endNano)
		rows, err := q.QueryContext(ctx, fmt.Sprintf(
			`SELECT series_id, ts_nano FROM %s WHERE series_id IN (%s) AND ts_nano >= ? AND ts_nano <= ?`,
			s.tables.pointValues, overlapWhere,
		), overlapArgs...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key sqliteV4PointKey
			if err := rows.Scan(&key.seriesID, &key.tsNano); err != nil {
				_ = rows.Close()
				return err
			}
			hotOverrides[key] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	add := func(series sqliteV4Series, timestamp int64, value float64) {
		bucketNano := floorDivNano(timestamp, query.Interval.Nanoseconds())
		key := foldedRollupKey(series.entityID, series.tagsHash, bucketNano, query.PreserveSeries)
		bucket := groups[key]
		if bucket == nil {
			bucket = newRollupBucketWithDigest(s.cfg.RollupPolicy.compression(), false)
			bucket.tagsHash = series.tagsHash
			bucket.tagsJSON = series.tagsJSON
			groups[key] = bucket
		}
		bucket.addMetricPoint(series.metricName, value, timestamp)
	}

	blockRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT b.series_id, b.start_nano, b.end_nano, b.point_count, b.codec, b.checksum, b.payload,
		        b.axis_id, a.codec, a.checksum, a.payload
		 FROM %s AS b LEFT JOIN %s AS a ON a.id = b.axis_id
		 WHERE b.series_id IN (%s) AND b.end_nano >= ? AND b.start_nano <= ?`,
		s.tables.pointBlocks, s.tables.pointAxes, seriesWhere,
	), blockArgs...)
	if err != nil {
		return err
	}
	for blockRows.Next() {
		var seriesID, blockStart, blockEnd int64
		var count, codec int
		var checksum int64
		var payload []byte
		var axisID, axisCodec, axisChecksum sql.NullInt64
		var axisPayload []byte
		if err := blockRows.Scan(&seriesID, &blockStart, &blockEnd, &count, &codec, &checksum, &payload, &axisID, &axisCodec, &axisChecksum, &axisPayload); err != nil {
			_ = blockRows.Close()
			return err
		}
		seriesItem := seriesByID[seriesID]
		first, last, err := s.visitSQLiteDashboardPointBlock(ctx, codec, count, uint32(checksum), payload,
			axisID, axisCodec, axisChecksum, axisPayload, func(timestamp int64, valueBits uint64) error {
				if timestamp < startNano || timestamp > endNano {
					return nil
				}
				if _, overridden := hotOverrides[sqliteV4PointKey{seriesID: seriesID, tsNano: timestamp}]; overridden {
					return nil
				}
				add(seriesItem, timestamp, math.Float64frombits(valueBits))
				return nil
			})
		if err != nil {
			_ = blockRows.Close()
			return fmt.Errorf("metric: decode SQLite V4 dashboard block series=%d start=%d: %w", seriesID, blockStart, err)
		}
		if first != blockStart || last != blockEnd {
			_ = blockRows.Close()
			return fmt.Errorf("metric: SQLite V4 dashboard block boundary mismatch for series=%d start=%d", seriesID, blockStart)
		}
	}
	if err := blockRows.Err(); err != nil {
		_ = blockRows.Close()
		return err
	}
	if err := blockRows.Close(); err != nil {
		return err
	}

	hotRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, ts_nano, value FROM %s WHERE series_id IN (%s) AND ts_nano >= ? AND ts_nano <= ?`,
		s.tables.pointValues, seriesWhere,
	), hotArgs...)
	if err != nil {
		return err
	}
	for hotRows.Next() {
		var seriesID, timestamp int64
		var value float64
		if err := hotRows.Scan(&seriesID, &timestamp, &value); err != nil {
			_ = hotRows.Close()
			return err
		}
		add(seriesByID[seriesID], timestamp, value)
	}
	if err := hotRows.Err(); err != nil {
		_ = hotRows.Close()
		return err
	}
	return hotRows.Close()
}
