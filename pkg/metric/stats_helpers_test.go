package metric

import "math"

// CalculateStats computes summary statistics for a point series.
//
// CalculateStats 基于一组点计算统计摘要，包括均值、百分位、首尾值和标准差。
func CalculateStats(points []Point) (Stats, error) {
	if len(points) == 0 {
		// Distinguish "the metric/range yielded no samples" from "the metric
		// definition does not exist" (which surfaces as ErrNotFound elsewhere).
		return Stats{}, ErrNoData
	}
	points = sortedPoints(points)
	avg, _ := aggregateValue(points, AggAvg)
	min, _ := aggregateValue(points, AggMin)
	max, _ := aggregateValue(points, AggMax)
	sum, _ := aggregateValue(points, AggSum)
	p50, _ := aggregateValue(points, AggP50)
	p95, _ := aggregateValue(points, AggP95)
	p99, _ := aggregateValue(points, AggP99)
	first, _ := aggregateValue(points, AggFirst)
	last, _ := aggregateValue(points, AggLast)
	rate, _ := aggregateValue(points, AggRate)

	var variance float64
	for _, p := range points {
		diff := p.Value - avg
		variance += diff * diff
	}

	return Stats{
		Count:  len(points),
		Min:    min,
		Max:    max,
		Avg:    avg,
		Sum:    sum,
		P50:    p50,
		P95:    p95,
		P99:    p99,
		First:  first,
		Last:   last,
		Rate:   rate,
		Start:  points[0].Timestamp,
		End:    points[len(points)-1].Timestamp,
		StdDev: math.Sqrt(variance / float64(len(points))),
	}, nil
}

func calculateMergedPingLatencyStats(points []Point) (Stats, error) {
	if len(points) == 0 {
		return Stats{}, ErrNoData
	}
	points = sortedPoints(points)
	valid := make([]Point, 0, len(points))
	for _, point := range points {
		if point.Value >= 0 {
			valid = append(valid, point)
		}
	}
	stats := Stats{
		Count: len(points),
		First: points[0].Value,
		Last:  points[len(points)-1].Value,
		Rate:  counterRate(points),
		Start: points[0].Timestamp,
		End:   points[len(points)-1].Timestamp,
	}
	if len(valid) == 0 {
		return stats, nil
	}
	validStats, err := CalculateStats(valid)
	if err != nil {
		return Stats{}, err
	}
	stats.Min = validStats.Min
	stats.Max = validStats.Max
	stats.Avg = validStats.Avg
	stats.Sum = validStats.Sum
	stats.P50 = validStats.P50
	stats.P95 = validStats.P95
	stats.P99 = validStats.P99
	stats.StdDev = validStats.StdDev
	return stats, nil
}
