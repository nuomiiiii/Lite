package metric

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

// PingDistribution is the whole-window percentile and population standard
// deviation of successful ping samples for one task.
type PingDistribution struct {
	P50    float64
	P99    float64
	StdDev float64
	OK     bool
}

// PingTaskDistributions returns one distribution per task_id.
// The output interval follows the rollup plan, so a window that is not an
// exact multiple of a tier still reads compacted history. A window still held
// as raw samples uses linear interpolation. Older samples that only remain in
// rollups are merged from bucket moments and digests.
func (s *Store) PingTaskDistributions(ctx context.Context, entityID string, start, end, now time.Time) (map[string]PingDistribution, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	start = start.UTC()
	end = end.UTC()
	now = now.UTC()
	interval := end.Sub(start)
	if interval <= 0 {
		interval = time.Second
	}
	interval = s.CompatibleSeriesIntervalForMetric(ctx, sqliteMergedPingLatencyMetric, start, now, interval)
	if interval <= 0 {
		interval = time.Second
	}
	probe := AggregateQuery{
		Query: Query{
			MetricName: sqliteMergedPingLatencyMetric,
			EntityID:   entityID,
			Start:      start,
			End:        end,
		},
		Interval:       interval,
		PreserveSeries: true,
	}
	select {
	case s.heavyReadGate <- struct{}{}:
		defer func() { <-s.heavyReadGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	rawOnly, err := s.seriesPhysicalUsesOnlyRaw(ctx, probe, now)
	if err != nil {
		return nil, err
	}
	if rawOnly {
		return s.pingTaskDistributionsFromRaw(ctx, entityID, start, end)
	}
	return s.pingTaskDistributionsFromMergedBuckets(ctx, probe, now)
}

func (s *Store) pingTaskDistributionsFromRaw(ctx context.Context, entityID string, start, end time.Time) (map[string]PingDistribution, error) {
	points, err := s.Query(ctx, Query{
		MetricName: sqliteMergedPingLatencyMetric,
		EntityID:   entityID,
		Start:      start,
		End:        end,
		Order:      OrderAsc,
	})
	if err != nil {
		return nil, err
	}
	grouped := make(map[string][]float64)
	for _, point := range points {
		if point.Value < 0 || math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
			continue
		}
		taskID := strings.TrimSpace(point.Tags["task_id"])
		if taskID == "" {
			continue
		}
		grouped[taskID] = append(grouped[taskID], point.Value)
	}
	out := make(map[string]PingDistribution, len(grouped))
	for taskID, values := range grouped {
		out[taskID] = pingDistributionFromValues(values)
	}
	return out, nil
}

func (s *Store) pingTaskDistributionsFromMergedBuckets(ctx context.Context, query AggregateQuery, now time.Time) (map[string]PingDistribution, error) {
	groups, err := s.collectSeriesPhysicalGroups(ctx, query, now, true)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]*rollupBucket)
	for _, bucket := range groups {
		if bucket == nil || bucket.count == 0 {
			continue
		}
		tags, err := rollupTagsFromJSON(bucket.tagsJSON)
		if err != nil {
			return nil, err
		}
		taskID := strings.TrimSpace(tags["task_id"])
		if taskID == "" {
			continue
		}
		target := merged[taskID]
		if target == nil {
			target = newRollupBucketWithDigest(defaultTDigestCompression, true)
			merged[taskID] = target
		}
		target.mergeStored(bucket)
	}
	out := make(map[string]PingDistribution, len(merged))
	for taskID, bucket := range merged {
		if bucket.count-bucket.lossCount <= 0 {
			continue
		}
		p50, ok50 := bucket.value(AggP50)
		p99, ok99 := bucket.value(AggP99)
		stddev, okDev := bucket.value(AggStdDev)
		if !ok50 || !ok99 || !okDev || bucket.digest == nil || bucket.digest.Count() == 0 {
			continue
		}
		out[taskID] = PingDistribution{P50: p50, P99: p99, StdDev: stddev, OK: true}
	}
	return out, nil
}

func pingDistributionFromValues(values []float64) PingDistribution {
	if len(values) == 0 {
		return PingDistribution{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var sum, sumSq float64
	for _, value := range sorted {
		sum += value
		sumSq += value * value
	}
	mean := sum / float64(len(sorted))
	variance := sumSq/float64(len(sorted)) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return PingDistribution{
		P50:    percentileSorted(sorted, 0.50),
		P99:    percentileSorted(sorted, 0.99),
		StdDev: math.Sqrt(variance),
		OK:     true,
	}
}
