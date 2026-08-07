package metric

import (
	"context"
	"fmt"
	"time"
)

type seriesBatchKey struct {
	metricName     string
	entityID       string
	start          int64
	end            int64
	tagsJSON       string
	interval       int64
	preserveSeries bool
}

type seriesBatchGroup struct {
	key        seriesBatchKey
	query      AggregateQuery
	indices    []int
	rawOnly    bool
	needDigest bool
	hasRate    bool
}

// seriesBatchWithinGate evaluates compatible aggregation views from a shared
// physical read. Raw-only groups from every metric and entity are loaded by a
// single QueryBatch call; rollup-backed groups share one collected bucket state
// across all requested aggregations of the same physical series.
func (s *Store) seriesBatchWithinGate(ctx context.Context, queries []AggregateQuery, now time.Time) ([][]AggregatePoint, error) {
	physical := append([]AggregateQuery(nil), queries...)
	virtualLoss := make([]bool, len(physical))
	for index := range physical {
		physical[index].Query = physical[index].Query.normalized()
		physical[index].Query.Limit = 0
		physical[index].Query.Offset = 0
		if s.sqlitePingMerged && physical[index].MetricName == sqliteVirtualPingLossMetric {
			virtualLoss[index] = true
			physical[index].MetricName = sqliteMergedPingLatencyMetric
			physical[index].Aggregation = pingLossPhysicalAggregation(physical[index].Aggregation)
		}
	}

	groups := make(map[seriesBatchKey]*seriesBatchGroup)
	order := make([]seriesBatchKey, 0, len(physical))
	for index, query := range physical {
		_, tagsJSON, err := tagsFingerprint(query.Tags)
		if err != nil {
			return nil, err
		}
		key := seriesBatchKey{
			metricName: query.MetricName, entityID: query.EntityID,
			start: query.Start.UnixNano(), end: query.End.UnixNano(), tagsJSON: tagsJSON,
			interval: query.Interval.Nanoseconds(), preserveSeries: query.PreserveSeries,
		}
		group := groups[key]
		if group == nil {
			group = &seriesBatchGroup{key: key, query: query}
			groups[key] = group
			order = append(order, key)
		}
		group.indices = append(group.indices, index)
		group.needDigest = group.needDigest || isPercentile(query.Aggregation)
		group.hasRate = group.hasRate || query.Aggregation == AggRate || query.Aggregation == aggPingLossRate
	}

	watermarks := make(map[string]seriesBatchWatermark)
	for _, key := range order {
		group := groups[key]
		rawOnly, err := s.seriesPhysicalUsesOnlyRawCached(ctx, group.query, now, watermarks)
		if err != nil {
			return nil, err
		}
		group.rawOnly = rawOnly || group.hasRate
	}

	result := make([][]AggregatePoint, len(queries))
	rawGroups := make([]*seriesBatchGroup, 0, len(groups))
	rawQueries := make([]Query, 0, len(groups))
	for _, key := range order {
		group := groups[key]
		if group.rawOnly {
			rawGroups = append(rawGroups, group)
			rawQueries = append(rawQueries, group.query.Query)
		}
	}
	if len(rawGroups) > 0 {
		rawPoints, err := s.queryBatchWithinGate(ctx, rawQueries, make([]bool, len(rawQueries)))
		if err != nil {
			return nil, err
		}
		for index, group := range rawGroups {
			if err := s.buildSeriesBatchGroup(result, physical, virtualLoss, group, rawPoints[index], nil); err != nil {
				return nil, err
			}
		}
	}

	rollupGroups := make([]*seriesBatchGroup, 0, len(groups))
	for _, key := range order {
		group := groups[key]
		if !group.rawOnly {
			rollupGroups = append(rollupGroups, group)
		}
	}
	if s.sqliteStorageV4 {
		for _, group := range rollupGroups {
			buckets, err := s.collectSeriesPhysicalGroups(ctx, group.query, now, group.needDigest)
			if err != nil {
				return nil, err
			}
			if err := s.buildSeriesBatchGroup(result, physical, virtualLoss, group, nil, buckets); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	collected, err := s.collectRelationalSeriesBatchGroups(ctx, rollupGroups, now, watermarks)
	if err != nil {
		return nil, err
	}
	for _, group := range rollupGroups {
		if err := s.buildSeriesBatchGroup(result, physical, virtualLoss, group, nil, collected[group]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) buildSeriesBatchGroup(result [][]AggregatePoint, physical []AggregateQuery, virtualLoss []bool, group *seriesBatchGroup, raw []Point, buckets map[rollupKey]*rollupBucket) error {
	for _, queryIndex := range group.indices {
		query := physical[queryIndex]
		var points []AggregatePoint
		var err error
		// Raw percentiles use the historical exact linear interpolation. T-digest
		// is reserved for stored rollups, where the original values no longer
		// exist. This keeps raw P50/P99 bit-for-bit compatible.
		if raw != nil && (query.Aggregation == AggRate || query.Aggregation == aggPingLossRate || isPercentile(query.Aggregation)) {
			points, err = AggregatePoints(raw, query)
		} else {
			if buckets == nil {
				buckets, err = foldRawPoints(nil, raw, group.query.Interval, s.cfg.RollupPolicy.compression(), group.query.PreserveSeries, false)
				if err != nil {
					return err
				}
			}
			points, err = rollupGroupsToPoints(buckets, query)
		}
		if err != nil {
			return fmt.Errorf("metric: build batch series %s/%s: %w", query.MetricName, query.Aggregation, err)
		}
		points = pageBuckets(points, query.BucketLimit, query.BucketOffset)
		if virtualLoss[queryIndex] {
			points = restoreVirtualPingLossAggregates(points)
		}
		result[queryIndex] = points
	}
	return nil
}

// PingSeriesSummaryBatch evaluates every latency summary and the packet-loss
// view for multiple entity filters through SeriesBatch. Results preserve the
// input order and the existing SeriesSummary contract.
func (s *Store) PingSeriesSummaryBatch(ctx context.Context, queries []AggregateQuery, now time.Time) ([]SeriesSummary, error) {
	flat := make([]AggregateQuery, 0, len(queries)*8)
	for _, base := range queries {
		base.MetricName = sqliteMergedPingLatencyMetric
		for _, aggregation := range []Aggregation{AggAvg, AggMin, AggMax, AggLast, AggP50, AggP99, AggStdDev} {
			view := base
			view.Aggregation = aggregation
			flat = append(flat, view)
		}
		loss := base
		loss.MetricName = sqliteVirtualPingLossMetric
		loss.Aggregation = AggAvg
		flat = append(flat, loss)
	}
	series, err := s.SeriesBatch(ctx, flat, now)
	if err != nil {
		return nil, err
	}
	result := make([]SeriesSummary, len(queries))
	for index := range result {
		base := index * 8
		result[index] = SeriesSummary{
			Avg: series[base], Min: series[base+1], Max: series[base+2], Last: series[base+3],
			P50: series[base+4], P99: series[base+5], StdDev: series[base+6], Loss: series[base+7],
		}
	}
	return result, nil
}
