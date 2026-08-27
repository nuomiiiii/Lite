package metric

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const dashboardBatchSeriesSize = 64

type dashboardBatchSpan struct {
	lower int64
	upper int64
}

type dashboardBatchPlan struct {
	query   AggregateQuery
	groups  map[rollupKey]*rollupBucket
	raw     *dashboardBatchSpan
	rollups map[int64]dashboardBatchSpan
}

type dashboardBatchSeries struct {
	series     sqliteV4Series
	queryIndex int
}

// DashboardSeriesBatch preserves DashboardSeries results while sharing one
// SQLite snapshot and routing decoded blocks to their requested metric.
func (s *Store) DashboardSeriesBatch(ctx context.Context, queries []AggregateQuery, now time.Time) ([][]AggregatePoint, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	for _, query := range queries {
		if err := query.Validate(); err != nil {
			return nil, err
		}
	}
	if len(queries) == 0 {
		return [][]AggregatePoint{}, nil
	}
	if !s.sqliteStorageV4 || !dashboardBatchAggregationsSupported(queries) {
		result := make([][]AggregatePoint, len(queries))
		for index, query := range queries {
			points, err := s.DashboardSeries(ctx, query, now)
			if err != nil {
				return nil, err
			}
			result[index] = points
		}
		return result, nil
	}
	ctx = withDashboardAxisQueryCache(ctx)

	select {
	case s.heavyReadGate <- struct{}{}:
		defer func() { <-s.heavyReadGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.dashboardSeriesBatchWithinGate(ctx, queries, now)
}

func dashboardBatchAggregationsSupported(queries []AggregateQuery) bool {
	for _, query := range queries {
		switch query.Aggregation {
		case AggAvg, AggSum, AggLast:
		default:
			return false
		}
	}
	return true
}

func (s *Store) dashboardSeriesBatchWithinGate(ctx context.Context, queries []AggregateQuery, now time.Time) ([][]AggregatePoint, error) {
	physical := append([]AggregateQuery(nil), queries...)
	virtualLoss := make([]bool, len(physical))
	for index := range physical {
		if s.sqlitePingMerged && physical[index].MetricName == sqliteVirtualPingLossMetric {
			virtualLoss[index] = true
			physical[index].MetricName = sqliteMergedPingLatencyMetric
			physical[index].Aggregation = pingLossPhysicalAggregation(physical[index].Aggregation)
		}
	}

	plans, optimized, err := s.planDashboardSeriesBatch(ctx, physical, now, virtualLoss)
	if err != nil {
		return nil, err
	}
	var result [][]AggregatePoint
	if optimized {
		result, err = s.executeDashboardSeriesBatch(ctx, plans)
	} else {
		result, err = s.dashboardSeriesBatchCompatibility(ctx, physical, now)
	}
	if err != nil {
		return nil, err
	}
	for index := range result {
		if virtualLoss[index] {
			result[index] = restoreVirtualPingLossAggregates(result[index])
		}
	}
	return result, nil
}

func (s *Store) dashboardSeriesBatchCompatibility(ctx context.Context, queries []AggregateQuery, now time.Time) ([][]AggregatePoint, error) {
	result := make([][]AggregatePoint, len(queries))
	for index, query := range queries {
		groups, err := s.collectSeriesPhysicalGroups(ctx, query, now, false)
		if err != nil {
			return nil, err
		}
		points, err := rollupGroupsToPoints(groups, query)
		if err != nil {
			return nil, err
		}
		result[index] = pageBuckets(points, query.BucketLimit, query.BucketOffset)
	}
	return result, nil
}

func (s *Store) planDashboardSeriesBatch(ctx context.Context, queries []AggregateQuery, now time.Time, virtualLoss []bool) ([]dashboardBatchPlan, bool, error) {
	seen := make(map[string]struct{}, len(queries))
	plans := make([]dashboardBatchPlan, len(queries))
	now = now.UTC()
	for index, query := range queries {
		if virtualLoss[index] || strings.TrimSpace(query.MetricName) == "" {
			return nil, false, nil
		}
		if _, exists := seen[query.MetricName]; exists {
			return nil, false, nil
		}
		seen[query.MetricName] = struct{}{}
		policy := s.rollupPolicyForMetric(ctx, query.MetricName)
		plan := dashboardBatchPlan{
			query:   query,
			groups:  make(map[rollupKey]*rollupBucket),
			rollups: make(map[int64]dashboardBatchSpan),
		}
		filter := query.Query.normalized()
		fullRaw := dashboardBatchSpan{lower: filter.Start.UnixNano(), upper: filter.End.UnixNano()}
		if !policy.Enabled() {
			plan.raw = &fullRaw
			plans[index] = plan
			continue
		}

		rawCutoff := policy.rawCutoff(now)
		if rawCutoff.IsZero() || !filter.Start.Before(rawCutoff) {
			plan.raw = &fullRaw
			plans[index] = plan
			continue
		}
		compatible := false
		for _, tier := range policy.Tiers {
			if query.Interval >= tier.Interval && query.Interval%tier.Interval == 0 {
				compatible = true
				break
			}
		}
		if !compatible {
			plan.raw = &fullRaw
			plans[index] = plan
			continue
		}

		watermark, hasWatermark, err := s.compactionWatermark(ctx, filter.MetricName)
		if err != nil {
			return nil, false, err
		}
		if !hasWatermark {
			return nil, false, nil
		}
		boundary := rawCutoff
		if watermark.Before(boundary) {
			boundary = watermark
		}
		if !filter.Start.Before(boundary) {
			plan.raw = &fullRaw
			plans[index] = plan
			continue
		}

		youngBoundary := boundary.UnixNano()
		for tierIndex, tier := range policy.Tiers {
			lower := alignRollupRetentionCutoff(now.Add(-tier.Retention), tier.Interval).UnixNano()
			if tierIndex+1 < len(policy.Tiers) {
				lower = alignRollupRetentionCutoff(now.Add(-tier.Retention), policy.Tiers[tierIndex+1].Interval).UnixNano()
			}
			if query.Interval < tier.Interval || query.Interval%tier.Interval != 0 {
				youngBoundary = lower
				continue
			}
			resolution := tier.Interval.Nanoseconds()
			scanLower := filter.Start.UnixNano()
			if lower > scanLower {
				scanLower = lower
			}
			scanUpper := filter.End.UnixNano() - resolution + 1
			if youngBoundary != math.MinInt64 && youngBoundary-1 < scanUpper {
				scanUpper = youngBoundary - 1
			}
			if scanUpper >= scanLower {
				plan.rollups[resolution] = dashboardBatchSpan{lower: scanLower, upper: scanUpper}
			}
			youngBoundary = lower
		}
		rawStart := boundary.UnixNano()
		if rawStart < filter.Start.UnixNano() {
			rawStart = filter.Start.UnixNano()
		}
		if rawStart <= filter.End.UnixNano() {
			span := dashboardBatchSpan{lower: rawStart, upper: filter.End.UnixNano()}
			plan.raw = &span
		}
		plans[index] = plan
	}
	return plans, true, nil
}

func (s *Store) executeDashboardSeriesBatch(ctx context.Context, plans []dashboardBatchPlan) ([][]AggregatePoint, error) {
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	series, err := s.dashboardBatchMatchingSeries(ctx, tx, plans)
	if err != nil {
		return nil, err
	}
	resolutions := make(map[int64]struct{})
	for _, plan := range plans {
		for resolution := range plan.rollups {
			resolutions[resolution] = struct{}{}
		}
	}
	orderedResolutions := make([]int64, 0, len(resolutions))
	for resolution := range resolutions {
		orderedResolutions = append(orderedResolutions, resolution)
	}
	sort.Slice(orderedResolutions, func(i, j int) bool { return orderedResolutions[i] < orderedResolutions[j] })
	for _, resolution := range orderedResolutions {
		if err := s.foldSQLiteV4DashboardRollupBatch(ctx, tx, series, plans, resolution); err != nil {
			return nil, err
		}
	}
	if err := s.foldSQLiteV4DashboardRawBatch(ctx, tx, series, plans); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	result := make([][]AggregatePoint, len(plans))
	for index, plan := range plans {
		points, err := rollupGroupsToPoints(plan.groups, plan.query)
		if err != nil {
			return nil, err
		}
		result[index] = pageBuckets(points, plan.query.BucketLimit, plan.query.BucketOffset)
	}
	return result, nil
}

func (s *Store) dashboardBatchMatchingSeries(ctx context.Context, q querier, plans []dashboardBatchPlan) ([]dashboardBatchSeries, error) {
	metricIndex := make(map[string]int, len(plans))
	names := make([]string, 0, len(plans))
	for index, plan := range plans {
		metricIndex[plan.query.MetricName] = index
		names = append(names, plan.query.MetricName)
	}
	sort.Strings(names)
	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for index, name := range names {
		placeholders[index] = "?"
		args[index] = name
	}
	rows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, metric_name, entity_id, tags_hash, tags FROM %s WHERE metric_name IN (%s) ORDER BY id`,
		s.tables.series, strings.Join(placeholders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]dashboardBatchSeries, 0)
	for rows.Next() {
		var item sqliteV4Series
		var rawTags any
		if err := rows.Scan(&item.id, &item.metricName, &item.entityID, &item.tagsHash, &rawTags); err != nil {
			return nil, err
		}
		item.tagsJSON, err = rawTagsToJSON(rawTags)
		if err != nil {
			return nil, err
		}
		item.tags, err = decodeMapString(item.tagsJSON)
		if err != nil {
			return nil, err
		}
		queryIndex := metricIndex[item.metricName]
		filter := plans[queryIndex].query.Query.normalized()
		if filter.EntityID != "" && filter.EntityID != item.entityID {
			continue
		}
		matched := true
		for key, value := range filter.Tags {
			if item.tags[key] != value {
				matched = false
				break
			}
		}
		if matched {
			result = append(result, dashboardBatchSeries{series: item, queryIndex: queryIndex})
		}
	}
	return result, rows.Err()
}

func dashboardBatchSeriesClause(series []dashboardBatchSeries) (string, []any) {
	placeholders := make([]string, len(series))
	args := make([]any, len(series))
	for index, item := range series {
		placeholders[index] = "?"
		args[index] = item.series.id
	}
	return strings.Join(placeholders, ","), args
}

func dashboardBatchBounds(series []dashboardBatchSeries, span func(dashboardBatchSeries) (dashboardBatchSpan, bool)) (int64, int64, bool) {
	var lower, upper int64
	found := false
	for _, item := range series {
		itemSpan, ok := span(item)
		if !ok {
			continue
		}
		if !found || itemSpan.lower < lower {
			lower = itemSpan.lower
		}
		if !found || itemSpan.upper > upper {
			upper = itemSpan.upper
		}
		found = true
	}
	return lower, upper, found
}

func (s *Store) foldSQLiteV4DashboardRawBatch(ctx context.Context, q querier, series []dashboardBatchSeries, plans []dashboardBatchPlan) error {
	rawSeries := series[:0]
	for _, item := range series {
		if plans[item.queryIndex].raw != nil {
			rawSeries = append(rawSeries, item)
		}
	}
	for start := 0; start < len(rawSeries); start += dashboardBatchSeriesSize {
		end := start + dashboardBatchSeriesSize
		if end > len(rawSeries) {
			end = len(rawSeries)
		}
		batch := rawSeries[start:end]
		if err := s.foldSQLiteV4DashboardRawBatchChunk(ctx, q, batch, plans); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) foldSQLiteV4DashboardRawBatchChunk(ctx context.Context, q querier, batch []dashboardBatchSeries, plans []dashboardBatchPlan) error {
	spanFor := func(item dashboardBatchSeries) (dashboardBatchSpan, bool) {
		span := plans[item.queryIndex].raw
		if span == nil {
			return dashboardBatchSpan{}, false
		}
		return *span, true
	}
	lower, upper, _ := dashboardBatchBounds(batch, spanFor)
	seriesByID := make(map[int64]dashboardBatchSeries, len(batch))
	for _, item := range batch {
		seriesByID[item.series.id] = item
	}
	seriesWhere, seriesArgs := dashboardBatchSeriesClause(batch)

	hotMinimum := make(map[int64]int64)
	hotArgs := append(append([]any{}, seriesArgs...), lower, upper)
	hotMinRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, MIN(ts_nano) FROM %s WHERE series_id IN (%s) AND ts_nano >= ? AND ts_nano <= ? GROUP BY series_id`,
		s.tables.pointValues, seriesWhere), hotArgs...)
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

	overlapping := make(map[int64]struct{})
	blockArgs := append(append([]any{}, seriesArgs...), lower, upper)
	blockMaxRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, MAX(end_nano) FROM %s WHERE series_id IN (%s) AND end_nano >= ? AND start_nano <= ? GROUP BY series_id`,
		s.tables.pointBlocks, seriesWhere), blockArgs...)
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
			overlapping[seriesID] = struct{}{}
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
	if len(overlapping) > 0 {
		overlap := make([]dashboardBatchSeries, 0, len(overlapping))
		for _, item := range batch {
			if _, ok := overlapping[item.series.id]; ok {
				overlap = append(overlap, item)
			}
		}
		overlapWhere, overlapArgs := dashboardBatchSeriesClause(overlap)
		overlapArgs = append(overlapArgs, lower, upper)
		rows, err := q.QueryContext(ctx, fmt.Sprintf(
			`SELECT series_id, ts_nano FROM %s WHERE series_id IN (%s) AND ts_nano >= ? AND ts_nano <= ?`,
			s.tables.pointValues, overlapWhere), overlapArgs...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key sqliteV4PointKey
			if err := rows.Scan(&key.seriesID, &key.tsNano); err != nil {
				_ = rows.Close()
				return err
			}
			span, _ := spanFor(seriesByID[key.seriesID])
			if key.tsNano >= span.lower && key.tsNano <= span.upper {
				hotOverrides[key] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}

	add := func(item dashboardBatchSeries, timestamp int64, value float64) {
		plan := &plans[item.queryIndex]
		bucketNano := floorDivNano(timestamp, plan.query.Interval.Nanoseconds())
		key := foldedRollupKey(item.series.entityID, item.series.tagsHash, bucketNano, plan.query.PreserveSeries)
		bucket := plan.groups[key]
		if bucket == nil {
			bucket = newRollupBucketWithDigest(s.cfg.RollupPolicy.compression(), false)
			bucket.tagsHash = item.series.tagsHash
			bucket.tagsJSON = item.series.tagsJSON
			plan.groups[key] = bucket
		}
		bucket.addMetricPoint(item.series.metricName, value, timestamp)
	}

	blockRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT b.series_id, b.start_nano, b.end_nano, b.point_count, b.codec, b.checksum, b.payload,
		        b.axis_id, a.codec, a.checksum, a.payload
		 FROM %s AS b LEFT JOIN %s AS a ON a.id = b.axis_id
		 WHERE b.series_id IN (%s) AND b.end_nano >= ? AND b.start_nano <= ?`,
		s.tables.pointBlocks, s.tables.pointAxes, seriesWhere), blockArgs...)
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
		first, last, err := s.visitSQLiteDashboardPointBlock(ctx, codec, count, uint32(checksum), payload,
			axisID, axisCodec, axisChecksum, axisPayload, func(timestamp int64, valueBits uint64) error {
				item := seriesByID[seriesID]
				span, _ := spanFor(item)
				if timestamp < span.lower || timestamp > span.upper {
					return nil
				}
				if _, overridden := hotOverrides[sqliteV4PointKey{seriesID: seriesID, tsNano: timestamp}]; overridden {
					return nil
				}
				add(item, timestamp, math.Float64frombits(valueBits))
				return nil
			})
		if err != nil {
			_ = blockRows.Close()
			return fmt.Errorf("metric: decode SQLite V4 dashboard batch block series=%d start=%d: %w", seriesID, blockStart, err)
		}
		if first != blockStart || last != blockEnd {
			_ = blockRows.Close()
			return fmt.Errorf("metric: SQLite V4 dashboard batch block boundary mismatch for series=%d start=%d", seriesID, blockStart)
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
		s.tables.pointValues, seriesWhere), hotArgs...)
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
		item := seriesByID[seriesID]
		span, _ := spanFor(item)
		if timestamp >= span.lower && timestamp <= span.upper {
			add(item, timestamp, value)
		}
	}
	if err := hotRows.Err(); err != nil {
		_ = hotRows.Close()
		return err
	}
	return hotRows.Close()
}

func (s *Store) foldSQLiteV4DashboardRollupBatch(ctx context.Context, q querier, series []dashboardBatchSeries, plans []dashboardBatchPlan, resolution int64) error {
	rollupSeries := make([]dashboardBatchSeries, 0, len(series))
	for _, item := range series {
		if _, ok := plans[item.queryIndex].rollups[resolution]; ok {
			rollupSeries = append(rollupSeries, item)
		}
	}
	for start := 0; start < len(rollupSeries); start += dashboardBatchSeriesSize {
		end := start + dashboardBatchSeriesSize
		if end > len(rollupSeries) {
			end = len(rollupSeries)
		}
		if err := s.foldSQLiteV4DashboardRollupBatchChunk(ctx, q, rollupSeries[start:end], plans, resolution); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) foldSQLiteV4DashboardRollupBatchChunk(ctx context.Context, q querier, batch []dashboardBatchSeries, plans []dashboardBatchPlan, resolution int64) error {
	spanFor := func(item dashboardBatchSeries) (dashboardBatchSpan, bool) {
		span, ok := plans[item.queryIndex].rollups[resolution]
		return span, ok
	}
	lower, upper, _ := dashboardBatchBounds(batch, spanFor)
	seriesByID := make(map[int64]dashboardBatchSeries, len(batch))
	for _, item := range batch {
		seriesByID[item.series.id] = item
	}
	seriesWhere, seriesArgs := dashboardBatchSeriesClause(batch)
	merge := func(item dashboardBatchSeries, record sqliteV4RollupRecord) {
		plan := &plans[item.queryIndex]
		key := foldedRollupKey(item.series.entityID, item.series.tagsHash,
			floorDivNano(record.bucketNano, plan.query.Interval.Nanoseconds()), plan.query.PreserveSeries)
		bucket := plan.groups[key]
		if bucket == nil {
			bucket = newRollupBucketWithDigest(s.cfg.RollupPolicy.compression(), false)
			bucket.tagsHash = item.series.tagsHash
			bucket.tagsJSON = item.series.tagsJSON
			plan.groups[key] = bucket
		}
		stored := rollupBucket{
			count: record.count, lossCount: record.lossCount,
			sum: math.Float64frombits(record.sumBits), sumSq: math.Float64frombits(record.sumSqBits),
			min: math.Float64frombits(record.minBits), max: math.Float64frombits(record.maxBits),
			firstVal: math.Float64frombits(record.firstBits), firstTS: record.firstTS,
			lastVal: math.Float64frombits(record.lastBits), lastTS: record.lastTS,
			tagsHash: item.series.tagsHash, tagsJSON: item.series.tagsJSON,
		}
		bucket.mergeStored(&stored)
	}
	type hotKey struct {
		seriesID int64
		bucket   int64
	}
	type hotRecord struct {
		seriesID int64
		record   sqliteV4RollupRecord
	}

	hotArgs := append(append([]any{}, seriesArgs...), resolution, lower, upper)
	hotRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, bucket_nano, count, loss_count, sum, sum_sq, min_val, max_val,
		        first_val, first_ts, last_val, last_ts
		 FROM %s WHERE series_id IN (%s) AND resolution_nano = ? AND bucket_nano >= ? AND bucket_nano <= ?
		 ORDER BY series_id, bucket_nano`, s.tables.rollupValues, seriesWhere), hotArgs...)
	if err != nil {
		return err
	}
	hotRecords := make([]hotRecord, 0)
	hotOverrides := make(map[hotKey]struct{})
	for hotRows.Next() {
		var seriesID int64
		var record sqliteV4RollupRecord
		var sum, sumSq, minValue, maxValue, firstValue, lastValue float64
		if err := hotRows.Scan(&seriesID, &record.bucketNano, &record.count, &record.lossCount,
			&sum, &sumSq, &minValue, &maxValue, &firstValue, &record.firstTS, &lastValue, &record.lastTS); err != nil {
			_ = hotRows.Close()
			return err
		}
		span, _ := spanFor(seriesByID[seriesID])
		if record.bucketNano < span.lower || record.bucketNano > span.upper {
			continue
		}
		record.sumBits = math.Float64bits(sum)
		record.sumSqBits = math.Float64bits(sumSq)
		record.minBits = math.Float64bits(minValue)
		record.maxBits = math.Float64bits(maxValue)
		record.firstBits = math.Float64bits(firstValue)
		record.lastBits = math.Float64bits(lastValue)
		hotRecords = append(hotRecords, hotRecord{seriesID: seriesID, record: record})
		hotOverrides[hotKey{seriesID: seriesID, bucket: record.bucketNano}] = struct{}{}
	}
	if err := hotRows.Err(); err != nil {
		_ = hotRows.Close()
		return err
	}
	if err := hotRows.Close(); err != nil {
		return err
	}

	blockArgs := append(append([]any{}, seriesArgs...), resolution, lower, upper)
	blockRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT b.series_id, b.start_nano, b.end_nano, b.bucket_count, b.codec, b.checksum, b.payload,
		        b.axis_id, a.codec, a.checksum, a.payload
		 FROM %s AS b LEFT JOIN %s AS a ON a.id = b.axis_id
		 WHERE b.series_id IN (%s) AND b.resolution_nano = ? AND b.end_nano >= ? AND b.start_nano <= ?
		 ORDER BY b.series_id, b.start_nano`,
		s.tables.rollupBlocks, s.tables.rollupAxes, seriesWhere), blockArgs...)
	if err != nil {
		return err
	}
	for blockRows.Next() {
		var seriesID, blockStart, blockEnd, checksum int64
		var count, codec int
		var payload, axisPayload []byte
		var axisID, axisCodec, axisChecksum sql.NullInt64
		if err := blockRows.Scan(&seriesID, &blockStart, &blockEnd, &count, &codec, &checksum, &payload,
			&axisID, &axisCodec, &axisChecksum, &axisPayload); err != nil {
			_ = blockRows.Close()
			return err
		}
		item := seriesByID[seriesID]
		span, _ := spanFor(item)
		first, last, err := s.visitSQLiteDashboardRollupBlock(ctx, codec, count, uint32(checksum), payload,
			axisID, axisCodec, axisChecksum, axisPayload, func(record sqliteV4RollupRecord) error {
				if record.bucketNano < span.lower || record.bucketNano > span.upper {
					return nil
				}
				if _, overridden := hotOverrides[hotKey{seriesID: seriesID, bucket: record.bucketNano}]; overridden {
					return nil
				}
				merge(item, record)
				return nil
			})
		if err != nil {
			_ = blockRows.Close()
			return fmt.Errorf("metric: decode SQLite V4 dashboard batch rollup series=%d start=%d: %w", seriesID, blockStart, err)
		}
		if first != blockStart || last != blockEnd {
			_ = blockRows.Close()
			return fmt.Errorf("metric: SQLite V4 dashboard batch rollup boundary mismatch for series=%d start=%d", seriesID, blockStart)
		}
	}
	if err := blockRows.Err(); err != nil {
		_ = blockRows.Close()
		return err
	}
	if err := blockRows.Close(); err != nil {
		return err
	}
	for _, hot := range hotRecords {
		merge(seriesByID[hot.seriesID], hot.record)
	}
	return nil
}
