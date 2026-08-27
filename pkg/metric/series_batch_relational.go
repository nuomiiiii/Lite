package metric

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type seriesBatchWatermark struct {
	value time.Time
	ok    bool
}

type relationalSeriesCollectPlan struct {
	group        *seriesBatchGroup
	buckets      map[rollupKey]*rollupBucket
	covered      map[string][]rollupCoverage
	hasWatermark bool
	rawQuery     *Query
}

type relationalSeriesRollupScan struct {
	plan       *relationalSeriesCollectPlan
	metricName string
	entityID   string
	tags       map[string]string
	resolution int64
	lower      int64
	upper      int64
	needDigest bool
}

type relationalSeriesRollupScanKey struct {
	resolution int64
	lower      int64
	upper      int64
	tagsJSON   string
	needDigest bool
}

func (s *Store) seriesPhysicalUsesOnlyRawCached(ctx context.Context, query AggregateQuery, now time.Time, cache map[string]seriesBatchWatermark) (bool, error) {
	policy := s.cfg.RollupPolicy
	if !policy.Enabled() {
		return true, nil
	}
	q := query.Query.normalized()
	now = now.UTC()
	policy = s.rollupPolicyForMetric(ctx, q.MetricName)
	rawCutoff := policy.rawCutoff(now)
	if rawCutoff.IsZero() || !q.Start.Before(rawCutoff) {
		return true, nil
	}
	compatible := false
	for _, tier := range policy.Tiers {
		if query.Interval >= tier.Interval && query.Interval%tier.Interval == 0 {
			compatible = true
			break
		}
	}
	if !compatible {
		return true, nil
	}
	watermark, err := s.seriesBatchCompactionWatermark(ctx, q.MetricName, cache)
	if err != nil {
		return false, err
	}
	if !watermark.ok {
		return false, nil
	}
	boundary := rawCutoff
	if watermark.value.Before(boundary) {
		boundary = watermark.value
	}
	return !q.Start.Before(boundary), nil
}

func (s *Store) seriesBatchCompactionWatermark(ctx context.Context, metricName string, cache map[string]seriesBatchWatermark) (seriesBatchWatermark, error) {
	if cached, ok := cache[metricName]; ok {
		return cached, nil
	}
	value, ok, err := s.compactionWatermark(ctx, metricName)
	if err != nil {
		return seriesBatchWatermark{}, err
	}
	result := seriesBatchWatermark{value: value, ok: ok}
	cache[metricName] = result
	return result, nil
}

func (s *Store) collectRelationalSeriesBatchGroups(ctx context.Context, groups []*seriesBatchGroup, now time.Time, watermarks map[string]seriesBatchWatermark) (map[*seriesBatchGroup]map[rollupKey]*rollupBucket, error) {
	result := make(map[*seriesBatchGroup]map[rollupKey]*rollupBucket, len(groups))
	if len(groups) == 0 {
		return result, nil
	}
	plans := make([]*relationalSeriesCollectPlan, 0, len(groups))
	scans := make([]relationalSeriesRollupScan, 0, len(groups)*len(s.cfg.RollupPolicy.Tiers))
	for _, group := range groups {
		plan, planScans, err := s.planRelationalSeriesCollect(ctx, group, now, watermarks)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
		scans = append(scans, planScans...)
		result[group] = plan.buckets
	}
	if err := s.executeRelationalSeriesRollupScans(ctx, scans); err != nil {
		return nil, err
	}
	if err := s.foldRelationalSeriesRawTails(ctx, plans); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) planRelationalSeriesCollect(ctx context.Context, group *seriesBatchGroup, now time.Time, watermarks map[string]seriesBatchWatermark) (*relationalSeriesCollectPlan, []relationalSeriesRollupScan, error) {
	query := group.query
	policy := s.rollupPolicyForMetric(ctx, query.MetricName)
	q := query.Query.normalized()
	now = now.UTC()
	rawBoundary := policy.rawCutoff(now)
	watermark, err := s.seriesBatchCompactionWatermark(ctx, q.MetricName, watermarks)
	if err != nil {
		return nil, nil, err
	}
	if watermark.ok && watermark.value.Before(rawBoundary) {
		rawBoundary = watermark.value
	}
	plan := &relationalSeriesCollectPlan{
		group: group, buckets: make(map[rollupKey]*rollupBucket),
		hasWatermark: watermark.ok,
	}
	if !watermark.ok {
		plan.covered = make(map[string][]rollupCoverage)
	}
	youngBoundary := rawBoundary.UTC().UnixNano()
	scans := make([]relationalSeriesRollupScan, 0, len(policy.Tiers))
	for index, tier := range policy.Tiers {
		lower := alignRollupRetentionCutoff(now.Add(-tier.Retention), tier.Interval).UnixNano()
		if index+1 < len(policy.Tiers) {
			lower = alignRollupRetentionCutoff(now.Add(-tier.Retention), policy.Tiers[index+1].Interval).UnixNano()
		}
		if query.Interval < tier.Interval || query.Interval%tier.Interval != 0 {
			youngBoundary = lower
			continue
		}
		resolution := tier.Interval.Nanoseconds()
		scanLower := q.Start.UnixNano()
		if lower > scanLower {
			scanLower = lower
		}
		scanUpper := q.End.UnixNano() - resolution + 1
		if youngBoundary != math.MinInt64 && youngBoundary-1 < scanUpper {
			scanUpper = youngBoundary - 1
		}
		if scanUpper >= scanLower {
			scans = append(scans, relationalSeriesRollupScan{
				plan: plan, metricName: q.MetricName, entityID: q.EntityID,
				tags: cloneStringMap(q.Tags), resolution: resolution,
				lower: scanLower, upper: scanUpper, needDigest: group.needDigest,
			})
		}
		youngBoundary = lower
	}
	rawStart := rawBoundary.UTC().UnixNano()
	if !watermark.ok || rawStart < q.Start.UnixNano() {
		rawStart = q.Start.UnixNano()
	}
	if rawStart <= q.End.UnixNano() {
		raw := q
		raw.Start = time.Unix(0, rawStart).UTC()
		raw.Limit = 0
		raw.Offset = 0
		plan.rawQuery = &raw
	}
	return plan, scans, nil
}

func (s *Store) executeRelationalSeriesRollupScans(ctx context.Context, scans []relationalSeriesRollupScan) error {
	grouped := make(map[relationalSeriesRollupScanKey][]*relationalSeriesRollupScan)
	order := make([]relationalSeriesRollupScanKey, 0)
	for index := range scans {
		_, tagsJSON, err := tagsFingerprint(scans[index].tags)
		if err != nil {
			return err
		}
		key := relationalSeriesRollupScanKey{
			resolution: scans[index].resolution, lower: scans[index].lower, upper: scans[index].upper,
			tagsJSON: tagsJSON, needDigest: scans[index].needDigest,
		}
		if _, ok := grouped[key]; !ok {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], &scans[index])
	}
	for _, key := range order {
		if err := s.executeRelationalSeriesRollupScanGroup(ctx, key, grouped[key]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) executeRelationalSeriesRollupScanGroup(ctx context.Context, key relationalSeriesRollupScanKey, scans []*relationalSeriesRollupScan) error {
	metricNames := uniqueRelationalRollupScanStrings(scans, func(scan *relationalSeriesRollupScan) string { return scan.metricName })
	entityIDs := uniqueRelationalRollupScanStrings(scans, func(scan *relationalSeriesRollupScan) string { return scan.entityID })
	entityWildcard := len(entityIDs) > 0 && entityIDs[0] == ""
	if entityWildcard {
		entityIDs = entityIDs[1:]
	}
	metricChunks := chunkBatchStrings(metricNames, relationalBatchChunkSize(s.cfg.Driver))
	entityChunks := [][]string{nil}
	if !entityWildcard {
		entityChunks = chunkBatchStrings(entityIDs, relationalBatchChunkSize(s.cfg.Driver))
	}
	for _, metricChunk := range metricChunks {
		for _, entityChunk := range entityChunks {
			if err := s.executeRelationalSeriesRollupScanChunk(ctx, key, scans, metricChunk, entityChunk, entityWildcard); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) executeRelationalSeriesRollupScanChunk(ctx context.Context, key relationalSeriesRollupScanKey, scans []*relationalSeriesRollupScan, metricNames, entityIDs []string, entityWildcard bool) error {
	args := []any{key.resolution, key.lower, key.upper}
	parts := []string{
		"resolution_nano = " + s.dialect.placeholder(1),
		"bucket_nano >= " + s.dialect.placeholder(2),
		"bucket_nano <= " + s.dialect.placeholder(3),
		"metric_name IN (" + appendBatchInClause(&args, metricNames, s.dialect) + ")",
	}
	if !entityWildcard {
		parts = append(parts, "entity_id IN ("+appendBatchInClause(&args, entityIDs, s.dialect)+")")
	}
	for _, tagKey := range sortedKeys(scans[0].tags) {
		args = append(args, scans[0].tags[tagKey])
		parts = append(parts, s.dialect.jsonExtractEquals("tags", tagKey, s.dialect.placeholder(len(args))))
	}
	columns := "metric_name, entity_id, tags_hash, tags, bucket_nano, count, sum, sum_sq, min_val, max_val, first_val, first_ts, last_val, last_ts"
	if key.needDigest {
		columns += ", digest"
	}
	observeMetricBatchScan(ctx, "rollup_relational")
	rows, err := s.reader().QueryContext(ctx, fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY metric_name ASC, entity_id ASC, bucket_nano ASC",
		columns, s.tables.rollups, strings.Join(parts, " AND ")), args...)
	if err != nil {
		return err
	}
	routes := relationalRollupScanRoutes(scans, metricNames, entityIDs, entityWildcard)
	for rows.Next() {
		var (
			metricName                            string
			entityID                              string
			tagsHash                              string
			rawTags                               any
			bucketNano                            int64
			count                                 int64
			sum, sumSq, minV, maxV, firstV, lastV float64
			firstTS, lastTS                       int64
			digestBlob                            []byte
		)
		scanArgs := []any{&metricName, &entityID, &tagsHash, &rawTags, &bucketNano, &count, &sum, &sumSq, &minV, &maxV, &firstV, &firstTS, &lastV, &lastTS}
		if key.needDigest {
			scanArgs = append(scanArgs, &digestBlob)
		}
		if err := rows.Scan(scanArgs...); err != nil {
			_ = rows.Close()
			return err
		}
		var digest *TDigest
		if key.needDigest {
			digest, err = DecodeTDigest(digestBlob)
			if err != nil {
				_ = rows.Close()
				return err
			}
		}
		tagsJSON, err := rawTagsToJSON(rawTags)
		if err != nil {
			_ = rows.Close()
			return err
		}
		row := storedRollup{
			entityID: entityID, bucket: bucketNano,
			bucketData: &rollupBucket{
				count: count, sum: sum, sumSq: sumSq, min: minV, max: maxV,
				firstVal: firstV, firstTS: firstTS, lastVal: lastV, lastTS: lastTS,
				digest: digest, tagsHash: tagsHash, tagsJSON: tagsJSON,
			},
		}
		for _, target := range relationalRollupScanRouteTargets(routes, metricName, entityID) {
			foldRollupRow(target.plan.buckets, row, target.plan.group.query.Interval, s.cfg.RollupPolicy.compression(), target.plan.group.query.PreserveSeries, target.needDigest)
			if !target.plan.hasWatermark {
				identity := entityID + "\x00" + tagsHash
				target.plan.covered[identity] = append(target.plan.covered[identity], rollupCoverage{start: bucketNano, end: bucketNano + target.resolution})
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func (s *Store) foldRelationalSeriesRawTails(ctx context.Context, plans []*relationalSeriesCollectPlan) error {
	rawPlans := make([]*relationalSeriesCollectPlan, 0, len(plans))
	rawQueries := make([]Query, 0, len(plans))
	for _, plan := range plans {
		if plan.rawQuery != nil {
			rawPlans = append(rawPlans, plan)
			rawQueries = append(rawQueries, *plan.rawQuery)
		}
	}
	if len(rawQueries) == 0 {
		return nil
	}
	pointsByPlan, err := s.queryBatchWithinGate(ctx, rawQueries, make([]bool, len(rawQueries)))
	if err != nil {
		return err
	}
	for index, plan := range rawPlans {
		points := pointsByPlan[index]
		if !plan.hasWatermark && len(plan.covered) > 0 {
			remaining := points[:0]
			for _, point := range points {
				tagsHash, _, err := tagsFingerprint(point.Tags)
				if err != nil {
					return err
				}
				identity := point.EntityID + "\x00" + tagsHash
				timestamp := point.Timestamp.UnixNano()
				covered := false
				for _, span := range plan.covered[identity] {
					if timestamp >= span.start && timestamp < span.end {
						covered = true
						break
					}
				}
				if !covered {
					remaining = append(remaining, point)
				}
			}
			points = remaining
		}
		if _, err := foldRawPoints(plan.buckets, points, plan.group.query.Interval, s.cfg.RollupPolicy.compression(), plan.group.query.PreserveSeries, plan.group.needDigest); err != nil {
			return err
		}
	}
	return nil
}

func uniqueRelationalRollupScanStrings(scans []*relationalSeriesRollupScan, value func(*relationalSeriesRollupScan) string) []string {
	seen := make(map[string]struct{}, len(scans))
	result := make([]string, 0, len(scans))
	for _, scan := range scans {
		item := value(scan)
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func relationalRollupScanRoutes(scans []*relationalSeriesRollupScan, metricNames, entityIDs []string, entityWildcard bool) map[string][]*relationalSeriesRollupScan {
	metricAllowed := make(map[string]struct{}, len(metricNames))
	for _, metricName := range metricNames {
		metricAllowed[metricName] = struct{}{}
	}
	entityAllowed := make(map[string]struct{}, len(entityIDs))
	for _, entityID := range entityIDs {
		entityAllowed[entityID] = struct{}{}
	}
	routes := make(map[string][]*relationalSeriesRollupScan)
	for _, scan := range scans {
		if _, ok := metricAllowed[scan.metricName]; !ok {
			continue
		}
		if !entityWildcard {
			if _, ok := entityAllowed[scan.entityID]; !ok {
				continue
			}
		}
		key := rawBatchRouteKey(scan.metricName, scan.entityID)
		routes[key] = append(routes[key], scan)
	}
	return routes
}

func relationalRollupScanRouteTargets(routes map[string][]*relationalSeriesRollupScan, metricName, entityID string) []*relationalSeriesRollupScan {
	direct := routes[rawBatchRouteKey(metricName, entityID)]
	wildcard := routes[rawBatchRouteKey(metricName, "")]
	if len(wildcard) == 0 || entityID == "" {
		return direct
	}
	result := make([]*relationalSeriesRollupScan, 0, len(direct)+len(wildcard))
	result = append(result, direct...)
	return append(result, wildcard...)
}
