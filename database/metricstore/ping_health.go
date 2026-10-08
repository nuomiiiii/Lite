package metricstore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nuomiiiii/lite/pkg/metric"
)

// PingHealthStats is the reusable ping window summary. Failed samples stored
// as -1 are counted as loss and excluded from latency statistics.
type PingHealthStats struct {
	Total             int64
	Lost              int64
	Successful        int64
	AverageLatencyMS  float64
	MinLatencyMS      float64
	MaxLatencyMS      float64
	HasLatency        bool
	FirstSuccessfulAt *time.Time
	LastSuccessfulAt  *time.Time
}

func (stats PingHealthStats) LossRate() float64 {
	if stats.Total == 0 {
		return 0
	}
	return float64(stats.Lost) / float64(stats.Total) * 100
}

func AdaptiveBaselineRange(now time.Time, latencyWindowSeconds, baselineWindowSeconds int, startedAt *time.Time) (start, end time.Time) {
	if latencyWindowSeconds < 0 {
		latencyWindowSeconds = 0
	}
	if baselineWindowSeconds < 0 {
		baselineWindowSeconds = 0
	}
	end = now.Add(-time.Duration(latencyWindowSeconds) * time.Second)
	if latencyWindowSeconds > 0 {
		// Metric queries treat End as inclusive; keep the current latency window
		// out of baseline history.
		end = end.Add(-time.Nanosecond)
	}
	start = end.Add(-time.Duration(baselineWindowSeconds) * time.Second)
	if startedAt != nil {
		bound := startedAt.UTC()
		if bound.After(start) {
			start = bound
		}
	}
	return start, end
}

func AdaptiveBaselineRangeValid(start, end time.Time) bool {
	return start.Before(end)
}

type PingBaselineCandidate struct {
	Successful int64
	MedianMS   float64
}

func QueryPingHealthStats(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time) (PingHealthStats, error) {
	if store == nil {
		return PingHealthStats{}, fmt.Errorf("metric store is not initialized")
	}
	loss, err := QueryPingLossStats(ctx, store, clientUUID, taskID, start, end)
	if err != nil {
		return PingHealthStats{}, err
	}
	avgPoints, err := queryPingLatencyWhole(ctx, store, clientUUID, taskID, start, end, metric.AggAvg)
	if err != nil {
		return PingHealthStats{}, err
	}
	minPoints, err := queryPingLatencyWhole(ctx, store, clientUUID, taskID, start, end, metric.AggMin)
	if err != nil {
		return PingHealthStats{}, err
	}
	maxPoints, err := queryPingLatencyWhole(ctx, store, clientUUID, taskID, start, end, metric.AggMax)
	if err != nil {
		return PingHealthStats{}, err
	}
	stats := CombinePingHealthStats(loss, avgPoints, minPoints, maxPoints)
	finePoints, err := queryPingLatencySeries(ctx, store, clientUUID, taskID, start, end, metric.AggAvg)
	if err != nil {
		return PingHealthStats{}, err
	}
	coverage := PingHealthStatsFromLatencyPoints(finePoints)
	stats.FirstSuccessfulAt = coverage.FirstSuccessfulAt
	stats.LastSuccessfulAt = coverage.LastSuccessfulAt
	return stats, nil
}

func QueryPingBaselineCandidate(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time) (PingBaselineCandidate, error) {
	if store == nil {
		return PingBaselineCandidate{}, fmt.Errorf("metric store is not initialized")
	}
	loss, err := QueryPingLossStats(ctx, store, clientUUID, taskID, start, end)
	if err != nil {
		return PingBaselineCandidate{}, err
	}
	points, err := queryPingLatencySeries(ctx, store, clientUUID, taskID, start, end, metric.AggP50)
	if err != nil {
		return PingBaselineCandidate{}, err
	}
	candidate := PingBaselineCandidateFromPoints(loss, points)
	if candidate.MedianMS > 0 || candidate.Successful == 0 {
		return candidate, nil
	}
	// Integer-millisecond samples on a sub-millisecond link have a median of 0.
	// A whole-window percentile can also come back as 0 when its digest is empty.
	// The average of successful samples still describes the latency the chart shows.
	avgPoints, err := queryPingLatencySeries(ctx, store, clientUUID, taskID, start, end, metric.AggAvg)
	if err != nil {
		return PingBaselineCandidate{}, err
	}
	if average := PingHealthStatsFromLatencyPoints(avgPoints).AverageLatencyMS; average > 0 {
		candidate.MedianMS = average
	}
	return candidate, nil
}

func queryPingLatencyWhole(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time, aggregation metric.Aggregation) ([]metric.AggregatePoint, error) {
	window := end.Sub(start)
	if window <= 0 {
		window = time.Second
	}
	return queryPingLatencySeriesWithInterval(ctx, store, clientUUID, taskID, start, end, aggregation, window)
}

func queryPingLatencySeries(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time, aggregation metric.Aggregation) ([]metric.AggregatePoint, error) {
	interval := store.CompatibleSeriesIntervalForMetric(ctx, MetricPingLatency, start, end, time.Second)
	if aggregation == metric.AggP50 {
		window := end.Sub(start)
		if window <= 0 {
			window = time.Second
		}
		interval = window
	}
	return queryPingLatencySeriesWithInterval(ctx, store, clientUUID, taskID, start, end, aggregation, interval)
}

func queryPingLatencySeriesWithInterval(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time, aggregation metric.Aggregation, interval time.Duration) ([]metric.AggregatePoint, error) {
	return store.Series(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: MetricPingLatency,
			EntityID:   clientUUID,
			Start:      start,
			End:        end,
			Order:      metric.OrderAsc,
			Tags:       map[string]string{"task_id": fmt.Sprintf("%d", taskID)},
		},
		Aggregation:    aggregation,
		Interval:       interval,
		PreserveSeries: true,
	}, end)
}

func CombinePingHealthStats(loss PingLossStats, avgPoints, minPoints, maxPoints []metric.AggregatePoint) PingHealthStats {
	stats := PingHealthStats{
		Total:      loss.Total,
		Lost:       loss.Lost,
		Successful: loss.Total - loss.Lost,
	}
	if stats.Successful < 0 {
		stats.Successful = 0
	}
	fromSamples := PingHealthStatsFromLatencyPoints(avgPoints)
	if stats.Successful > 0 && fromSamples.HasLatency {
		stats.AverageLatencyMS = fromSamples.AverageLatencyMS
		stats.HasLatency = true
		stats.FirstSuccessfulAt = fromSamples.FirstSuccessfulAt
		stats.LastSuccessfulAt = fromSamples.LastSuccessfulAt
	}
	if stats.Successful > 0 {
		if minVal, ok := positiveExtreme(minPoints, true); ok {
			stats.MinLatencyMS = RoundLatencyMS(minVal)
			stats.HasLatency = true
		}
		if maxVal, ok := positiveExtreme(maxPoints, false); ok {
			stats.MaxLatencyMS = RoundLatencyMS(maxVal)
			stats.HasLatency = true
		}
	}
	return stats
}

func PingHealthStatsFromLatencyPoints(points []metric.AggregatePoint) PingHealthStats {
	var stats PingHealthStats
	var weightedSum float64
	for _, point := range points {
		if point.Count <= 0 {
			continue
		}
		count := int64(point.Count)
		stats.Total += count
		if point.Value < 0 {
			stats.Lost += count
			continue
		}
		stats.Successful += count
		weightedSum += point.Value * float64(count)
		if !stats.HasLatency || point.Value < stats.MinLatencyMS {
			stats.MinLatencyMS = point.Value
		}
		if !stats.HasLatency || point.Value > stats.MaxLatencyMS {
			stats.MaxLatencyMS = point.Value
		}
		bucket := point.Bucket.UTC()
		if stats.FirstSuccessfulAt == nil || bucket.Before(*stats.FirstSuccessfulAt) {
			ts := bucket
			stats.FirstSuccessfulAt = &ts
		}
		if stats.LastSuccessfulAt == nil || bucket.After(*stats.LastSuccessfulAt) {
			ts := bucket
			stats.LastSuccessfulAt = &ts
		}
		stats.HasLatency = true
	}
	if stats.Successful > 0 {
		stats.AverageLatencyMS = RoundLatencyMS(weightedSum / float64(stats.Successful))
		stats.MinLatencyMS = RoundLatencyMS(stats.MinLatencyMS)
		stats.MaxLatencyMS = RoundLatencyMS(stats.MaxLatencyMS)
	}
	return stats
}

// RoundLatencyMS keeps latency figures at hundredths of a millisecond.
// 0.00 is not a usable latency: comparisons and stored values share this precision.
func RoundLatencyMS(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	return math.Round(value*100) / 100
}

// LatencyComparisonBand rounds a latency interval to 0.01 ms.
// A usable raw interval that collapses to one centisecond is widened by 0.01 ms
// around anchor, so comparison still has an inside and an outside.
func LatencyComparisonBand(rawLow, rawHigh, anchor float64) (low, high float64, ok bool) {
	if !(rawHigh > rawLow) || math.IsNaN(rawLow) || math.IsNaN(rawHigh) || math.IsNaN(anchor) {
		return RoundLatencyMS(rawLow), RoundLatencyMS(rawHigh), false
	}
	low = RoundLatencyMS(rawLow)
	high = RoundLatencyMS(rawHigh)
	if high > low {
		return low, high, true
	}
	center := RoundLatencyMS(anchor)
	low = RoundLatencyMS(center - 0.01)
	high = RoundLatencyMS(center + 0.01)
	if high <= low {
		high = RoundLatencyMS(low + 0.01)
	}
	return low, high, high > low
}

func PingBaselineCandidateFromPoints(loss PingLossStats, points []metric.AggregatePoint) PingBaselineCandidate {
	candidate := PingBaselineCandidate{Successful: loss.Total - loss.Lost}
	if candidate.Successful < 0 {
		candidate.Successful = 0
	}
	values := make([]float64, 0, len(points))
	for _, point := range points {
		if point.Count <= 0 || point.Value < 0 || math.IsNaN(point.Value) {
			continue
		}
		values = append(values, point.Value)
	}
	if median, ok := medianFloats(values); ok && median > 0 {
		candidate.MedianMS = RoundLatencyMS(median)
	}
	if candidate.Successful == 0 {
		fromSamples := PingHealthStatsFromLatencyPoints(points)
		candidate.Successful = fromSamples.Successful
	}
	return candidate
}

func PingBaselineCandidateFromValues(values []float64) PingBaselineCandidate {
	filtered := make([]float64, 0, len(values))
	for _, value := range values {
		if value < 0 || math.IsNaN(value) {
			continue
		}
		filtered = append(filtered, value)
	}
	candidate := PingBaselineCandidate{Successful: int64(len(filtered))}
	if median, ok := medianFloats(filtered); ok {
		candidate.MedianMS = RoundLatencyMS(median)
	}
	return candidate
}

func positiveExtreme(points []metric.AggregatePoint, wantMin bool) (float64, bool) {
	found := false
	extreme := 0.0
	for _, point := range points {
		if point.Count <= 0 || point.Value < 0 || math.IsNaN(point.Value) {
			continue
		}
		if !found {
			extreme = point.Value
			found = true
			continue
		}
		if wantMin && point.Value < extreme {
			extreme = point.Value
		}
		if !wantMin && point.Value > extreme {
			extreme = point.Value
		}
	}
	return extreme, found
}

func medianFloats(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid], true
	}
	return (sorted[mid-1] + sorted[mid]) / 2, true
}
