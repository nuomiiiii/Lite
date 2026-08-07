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

const rawBatchSeriesSize = 64

type rawBatchGroupKey struct {
	start    int64
	end      int64
	order    Order
	tagsJSON string
}

type rawBatchGroup struct {
	key     rawBatchGroupKey
	tags    map[string]string
	indices []int
}

type rawBatchSeries struct {
	series       sqliteV4Series
	queryIndices []int
}

// QueryBatch evaluates compatible raw queries with one physical scan (or a
// bounded number of parameter chunks). Results preserve input order and each
// query's independent paging semantics.
func (s *Store) QueryBatch(ctx context.Context, queries []Query) ([][]Point, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	normalized := make([]Query, len(queries))
	virtualLoss := make([]bool, len(queries))
	for index, query := range queries {
		if err := query.Validate(); err != nil {
			return nil, err
		}
		query = query.normalized()
		if s.sqlitePingMerged && query.MetricName == sqliteVirtualPingLossMetric {
			virtualLoss[index] = true
			query.MetricName = sqliteMergedPingLatencyMetric
		}
		normalized[index] = query
	}
	if len(normalized) == 0 {
		return [][]Point{}, nil
	}

	select {
	case s.heavyReadGate <- struct{}{}:
		defer func() { <-s.heavyReadGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.queryBatchWithinGate(ctx, normalized, virtualLoss)
}

func (s *Store) queryBatchWithinGate(ctx context.Context, queries []Query, virtualLoss []bool) ([][]Point, error) {
	groups, err := groupRawBatchQueries(queries)
	if err != nil {
		return nil, err
	}
	result := make([][]Point, len(queries))
	for _, group := range groups {
		if s.sqliteStorageV4 {
			err = s.querySQLiteV4BatchGroup(ctx, group, queries, virtualLoss, result)
		} else {
			err = s.queryRelationalBatchGroup(ctx, group, queries, virtualLoss, result)
		}
		if err != nil {
			return nil, err
		}
	}
	for index, query := range queries {
		sortRawBatchPoints(result[index], query.Order)
		result[index] = pageRawBatchPoints(result[index], query.Limit, query.Offset)
	}
	return result, nil
}

func groupRawBatchQueries(queries []Query) ([]rawBatchGroup, error) {
	groups := make(map[rawBatchGroupKey]*rawBatchGroup)
	order := make([]rawBatchGroupKey, 0)
	for index, query := range queries {
		_, tagsJSON, err := tagsFingerprint(query.Tags)
		if err != nil {
			return nil, err
		}
		key := rawBatchGroupKey{
			start: query.Start.UnixNano(), end: query.End.UnixNano(),
			order: query.Order, tagsJSON: tagsJSON,
		}
		group := groups[key]
		if group == nil {
			group = &rawBatchGroup{key: key, tags: cloneStringMap(query.Tags)}
			groups[key] = group
			order = append(order, key)
		}
		group.indices = append(group.indices, index)
	}
	result := make([]rawBatchGroup, 0, len(order))
	for _, key := range order {
		result = append(result, *groups[key])
	}
	return result, nil
}

func (s *Store) queryRelationalBatchGroup(ctx context.Context, group rawBatchGroup, queries []Query, virtualLoss []bool, result [][]Point) error {
	metricNames := uniqueBatchStrings(group.indices, func(index int) string { return queries[index].MetricName })
	entityIDs, hasWildcard := uniqueBatchEntities(group.indices, queries)
	metricChunks := chunkBatchStrings(metricNames, relationalBatchChunkSize(s.cfg.Driver))
	entityChunks := [][]string{nil}
	if !hasWildcard {
		entityChunks = chunkBatchStrings(entityIDs, relationalBatchChunkSize(s.cfg.Driver))
	}
	for _, metricChunk := range metricChunks {
		for _, entityChunk := range entityChunks {
			if err := s.queryRelationalBatchChunk(ctx, group, queries, virtualLoss, result, metricChunk, entityChunk, hasWildcard); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) queryRelationalBatchChunk(ctx context.Context, group rawBatchGroup, queries []Query, virtualLoss []bool, result [][]Point, metricNames, entityIDs []string, entityWildcard bool) error {
	args := make([]any, 0, 2+len(metricNames)+len(entityIDs)+len(group.tags))
	args = append(args, group.key.start, group.key.end)
	parts := []string{
		"ts_nano >= " + s.dialect.placeholder(1),
		"ts_nano <= " + s.dialect.placeholder(2),
	}
	metricClause := appendBatchInClause(&args, metricNames, s.dialect)
	parts = append(parts, "metric_name IN ("+metricClause+")")
	if !entityWildcard {
		entityClause := appendBatchInClause(&args, entityIDs, s.dialect)
		parts = append(parts, "entity_id IN ("+entityClause+")")
	}
	for _, key := range sortedKeys(group.tags) {
		args = append(args, group.tags[key])
		parts = append(parts, s.dialect.jsonExtractEquals("tags", key, s.dialect.placeholder(len(args))))
	}
	order := "ASC"
	if group.key.order == OrderDesc {
		order = "DESC"
	}
	observeMetricBatchScan(ctx, "raw_relational")
	rows, err := s.reader().QueryContext(ctx, fmt.Sprintf(
		`SELECT metric_name, entity_id, ts_nano, value, tags, labels FROM %s WHERE %s ORDER BY ts_nano %s, metric_name ASC, entity_id ASC`,
		s.tables.points, strings.Join(parts, " AND "), order), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	routes := rawBatchRoutes(group.indices, queries, metricNames, entityIDs, entityWildcard)
	for rows.Next() {
		var point Point
		var timestamp int64
		var rawTags, rawLabels any
		if err := rows.Scan(&point.MetricName, &point.EntityID, &timestamp, &point.Value, &rawTags, &rawLabels); err != nil {
			return err
		}
		point.Timestamp = unixNanoUTC(timestamp)
		point.Tags, err = decodeMap(rawTags)
		if err != nil {
			return err
		}
		point.Labels, err = decodeMap(rawLabels)
		if err != nil {
			return err
		}
		for _, queryIndex := range rawBatchRouteIndices(routes, point.MetricName, point.EntityID) {
			candidate := point
			candidate.Tags = cloneStringMap(point.Tags)
			candidate.Labels = cloneStringMap(point.Labels)
			if virtualLoss[queryIndex] {
				candidate = virtualPingLossPoint(candidate)
			}
			result[queryIndex] = append(result[queryIndex], candidate)
		}
	}
	return rows.Err()
}

func (s *Store) querySQLiteV4BatchGroup(ctx context.Context, group rawBatchGroup, queries []Query, virtualLoss []bool, result [][]Point) error {
	tx, err := s.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	series, err := s.sqliteV4RawBatchSeries(ctx, tx, group, queries)
	if err != nil {
		return err
	}
	for start := 0; start < len(series); start += rawBatchSeriesSize {
		end := start + rawBatchSeriesSize
		if end > len(series) {
			end = len(series)
		}
		if err := s.querySQLiteV4BatchSeriesChunk(ctx, tx, group, series[start:end], virtualLoss, result); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) sqliteV4RawBatchSeries(ctx context.Context, q querier, group rawBatchGroup, queries []Query) ([]rawBatchSeries, error) {
	metricNames := uniqueBatchStrings(group.indices, func(index int) string { return queries[index].MetricName })
	entityIDs, hasWildcard := uniqueBatchEntities(group.indices, queries)
	args := make([]any, 0, len(metricNames)+len(entityIDs)+len(group.tags))
	parts := []string{"metric_name IN (" + appendBatchSQLiteInClause(&args, metricNames) + ")"}
	if !hasWildcard {
		parts = append(parts, "entity_id IN ("+appendBatchSQLiteInClause(&args, entityIDs)+")")
	}
	for _, key := range sortedKeys(group.tags) {
		args = append(args, group.tags[key])
		parts = append(parts, s.dialect.jsonExtractEquals("tags", key, "?"))
	}
	observeMetricBatchScan(ctx, "raw_sqlite_series")
	rows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, metric_name, entity_id, tags_hash, tags FROM %s WHERE %s ORDER BY id`,
		s.tables.series, strings.Join(parts, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	routes := rawBatchRoutes(group.indices, queries, metricNames, entityIDs, hasWildcard)
	result := make([]rawBatchSeries, 0)
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
		indices := rawBatchRouteIndices(routes, item.metricName, item.entityID)
		if len(indices) > 0 {
			result = append(result, rawBatchSeries{series: item, queryIndices: indices})
		}
	}
	return result, rows.Err()
}

func (s *Store) querySQLiteV4BatchSeriesChunk(ctx context.Context, q querier, group rawBatchGroup, batch []rawBatchSeries, virtualLoss []bool, result [][]Point) error {
	if len(batch) == 0 {
		return nil
	}
	series := make([]sqliteV4Series, len(batch))
	seriesByID := make(map[int64]rawBatchSeries, len(batch))
	for index, item := range batch {
		series[index] = item.series
		seriesByID[item.series.id] = item
	}
	seriesWhere, seriesArgs := sqliteV4SeriesIDClause(series)
	stored := make(map[sqliteV4PointKey]sqliteV4StoredPoint)
	blockArgs := append(append([]any{}, seriesArgs...), group.key.start, group.key.end)
	observeMetricBatchScan(ctx, "raw_sqlite_blocks")
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
		var seriesID, blockStart, blockEnd, checksum int64
		var count, codec int
		var payload, axisPayload []byte
		var axisID, axisCodec, axisChecksum sql.NullInt64
		if err := blockRows.Scan(&seriesID, &blockStart, &blockEnd, &count, &codec, &checksum, &payload, &axisID, &axisCodec, &axisChecksum, &axisPayload); err != nil {
			_ = blockRows.Close()
			return err
		}
		points, err := s.decodeSQLitePointBlockCached(codec, count, uint32(checksum), payload, axisID, axisCodec, axisChecksum, axisPayload)
		if err != nil {
			_ = blockRows.Close()
			return err
		}
		if len(points) == 0 || points[0].timestamp != blockStart || points[len(points)-1].timestamp != blockEnd {
			_ = blockRows.Close()
			return fmt.Errorf("metric: SQLite V4 batch block boundary mismatch for series=%d start=%d", seriesID, blockStart)
		}
		for _, point := range points {
			if point.timestamp >= group.key.start && point.timestamp <= group.key.end {
				stored[sqliteV4PointKey{seriesID: seriesID, tsNano: point.timestamp}] = sqliteV4StoredPoint{series: seriesByID[seriesID].series, block: point}
			}
		}
	}
	if err := blockRows.Err(); err != nil {
		_ = blockRows.Close()
		return err
	}
	if err := blockRows.Close(); err != nil {
		return err
	}

	hotArgs := append(append([]any{}, seriesArgs...), group.key.start, group.key.end)
	observeMetricBatchScan(ctx, "raw_sqlite_hot")
	hotRows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, ts_nano, value, labels, created_at FROM %s
		 WHERE series_id IN (%s) AND ts_nano >= ? AND ts_nano <= ?`,
		s.tables.pointValues, seriesWhere), hotArgs...)
	if err != nil {
		return err
	}
	for hotRows.Next() {
		var seriesID int64
		var point sqliteV4BlockPoint
		var value float64
		var rawLabels any
		if err := hotRows.Scan(&seriesID, &point.timestamp, &value, &rawLabels, &point.createdAt); err != nil {
			_ = hotRows.Close()
			return err
		}
		point.valueBits = math.Float64bits(value)
		point.labels, err = rawTagsToJSON(rawLabels)
		if err != nil {
			_ = hotRows.Close()
			return err
		}
		stored[sqliteV4PointKey{seriesID: seriesID, tsNano: point.timestamp}] = sqliteV4StoredPoint{series: seriesByID[seriesID].series, block: point}
	}
	if err := hotRows.Err(); err != nil {
		_ = hotRows.Close()
		return err
	}
	if err := hotRows.Close(); err != nil {
		return err
	}
	for key, storedPoint := range stored {
		item := seriesByID[key.seriesID]
		labels, err := decodeMapString(storedPoint.block.labels)
		if err != nil {
			return err
		}
		point := Point{
			MetricName: item.series.metricName, EntityID: item.series.entityID,
			Timestamp: unixNanoUTC(storedPoint.block.timestamp),
			Value:     math.Float64frombits(storedPoint.block.valueBits),
			Tags:      cloneStringMap(item.series.tags), Labels: labels,
		}
		for _, queryIndex := range item.queryIndices {
			candidate := point
			candidate.Tags = cloneStringMap(point.Tags)
			candidate.Labels = cloneStringMap(point.Labels)
			if virtualLoss[queryIndex] {
				candidate = virtualPingLossPoint(candidate)
			}
			result[queryIndex] = append(result[queryIndex], candidate)
		}
	}
	return nil
}

func rawBatchRoutes(indices []int, queries []Query, metricNames, entityIDs []string, entityWildcard bool) map[string][]int {
	metricAllowed := make(map[string]struct{}, len(metricNames))
	for _, name := range metricNames {
		metricAllowed[name] = struct{}{}
	}
	entityAllowed := make(map[string]struct{}, len(entityIDs))
	for _, id := range entityIDs {
		entityAllowed[id] = struct{}{}
	}
	routes := make(map[string][]int)
	for _, index := range indices {
		query := queries[index]
		if _, ok := metricAllowed[query.MetricName]; !ok {
			continue
		}
		if !entityWildcard {
			if _, ok := entityAllowed[query.EntityID]; !ok {
				continue
			}
		}
		key := rawBatchRouteKey(query.MetricName, query.EntityID)
		routes[key] = append(routes[key], index)
	}
	return routes
}

func rawBatchRouteKey(metricName, entityID string) string {
	return metricName + "\x00" + entityID
}

func rawBatchRouteIndices(routes map[string][]int, metricName, entityID string) []int {
	direct := routes[rawBatchRouteKey(metricName, entityID)]
	if entityID == "" {
		return direct
	}
	wildcard := routes[rawBatchRouteKey(metricName, "")]
	if len(wildcard) == 0 {
		return direct
	}
	result := make([]int, 0, len(direct)+len(wildcard))
	result = append(result, direct...)
	return append(result, wildcard...)
}

func uniqueBatchStrings(indices []int, value func(int) string) []string {
	seen := make(map[string]struct{}, len(indices))
	result := make([]string, 0, len(indices))
	for _, index := range indices {
		item := value(index)
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func uniqueBatchEntities(indices []int, queries []Query) ([]string, bool) {
	hasWildcard := false
	entities := uniqueBatchStrings(indices, func(index int) string {
		if queries[index].EntityID == "" {
			hasWildcard = true
		}
		return queries[index].EntityID
	})
	if len(entities) > 0 && entities[0] == "" {
		entities = entities[1:]
	}
	return entities, hasWildcard
}

func relationalBatchChunkSize(driver Driver) int {
	return 400
}

func chunkBatchStrings(values []string, size int) [][]string {
	if len(values) == 0 {
		return [][]string{nil}
	}
	result := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		result = append(result, values[start:end])
	}
	return result
}

func appendBatchInClause(args *[]any, values []string, dialect dialect) string {
	placeholders := make([]string, len(values))
	for index, value := range values {
		*args = append(*args, value)
		placeholders[index] = dialect.placeholder(len(*args))
	}
	return strings.Join(placeholders, ",")
}

func appendBatchSQLiteInClause(args *[]any, values []string) string {
	placeholders := make([]string, len(values))
	for index, value := range values {
		*args = append(*args, value)
		placeholders[index] = "?"
	}
	return strings.Join(placeholders, ",")
}

func sortRawBatchPoints(points []Point, order Order) {
	sort.Slice(points, func(i, j int) bool {
		if !points[i].Timestamp.Equal(points[j].Timestamp) {
			if order == OrderDesc {
				return points[i].Timestamp.After(points[j].Timestamp)
			}
			return points[i].Timestamp.Before(points[j].Timestamp)
		}
		if points[i].EntityID != points[j].EntityID {
			return points[i].EntityID < points[j].EntityID
		}
		leftHash, _, _ := tagsFingerprint(points[i].Tags)
		rightHash, _, _ := tagsFingerprint(points[j].Tags)
		return leftHash < rightHash
	})
}

func pageRawBatchPoints(points []Point, limit, offset int) []Point {
	start := offset
	if start > len(points) {
		start = len(points)
	}
	end := len(points)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return points[start:end]
}

func unixNanoUTC(timestamp int64) (resultTime time.Time) {
	return time.Unix(0, timestamp).UTC()
}
