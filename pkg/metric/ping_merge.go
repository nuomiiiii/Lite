package metric

import (
	"math"
	"strings"
)

const (
	sqliteMergedPingLatencyMetric = "ping.latency_ms"
	sqliteVirtualPingLossMetric   = "ping.loss"

	aggPingLossAvg    Aggregation = "__ping_loss_avg"
	aggPingLossSum    Aggregation = "__ping_loss_sum"
	aggPingLossMin    Aggregation = "__ping_loss_min"
	aggPingLossMax    Aggregation = "__ping_loss_max"
	aggPingLossFirst  Aggregation = "__ping_loss_first"
	aggPingLossLast   Aggregation = "__ping_loss_last"
	aggPingLossStdDev Aggregation = "__ping_loss_stddev"
	aggPingLossRate   Aggregation = "__ping_loss_rate"
	aggPingLossPrefix             = "__ping_loss_percentile:"
)

func pingLossPhysicalAggregation(aggregation Aggregation) Aggregation {
	switch aggregation {
	case AggAvg:
		return aggPingLossAvg
	case AggSum:
		return aggPingLossSum
	case AggMin:
		return aggPingLossMin
	case AggMax:
		return aggPingLossMax
	case AggFirst:
		return aggPingLossFirst
	case AggLast:
		return aggPingLossLast
	case AggStdDev:
		return aggPingLossStdDev
	case AggRate:
		return aggPingLossRate
	case AggCount:
		return AggCount
	default:
		if _, ok := parsePercentile(aggregation); ok {
			return Aggregation(aggPingLossPrefix + string(aggregation))
		}
		return aggregation
	}
}

func isInternalPingLossAggregation(aggregation Aggregation) bool {
	switch aggregation {
	case aggPingLossAvg, aggPingLossSum, aggPingLossMin, aggPingLossMax,
		aggPingLossFirst, aggPingLossLast, aggPingLossStdDev, aggPingLossRate:
		return true
	default:
		_, ok := parsePingLossPercentile(aggregation)
		return ok
	}
}

func parsePingLossPercentile(aggregation Aggregation) (float64, bool) {
	value := string(aggregation)
	if !strings.HasPrefix(value, aggPingLossPrefix) {
		return 0, false
	}
	return parsePercentile(Aggregation(strings.TrimPrefix(value, aggPingLossPrefix)))
}

func pingLossPercentile(total, loss int64, fraction float64) float64 {
	if total <= 0 {
		return 0
	}
	if loss <= 0 {
		return 0
	}
	if loss >= total {
		return 1
	}
	position := fraction * float64(total-1)
	lower := int64(math.Floor(position))
	upper := int64(math.Ceil(position))
	zeros := total - loss
	lowerValue := 0.0
	if lower >= zeros {
		lowerValue = 1
	}
	upperValue := 0.0
	if upper >= zeros {
		upperValue = 1
	}
	if lower == upper {
		return lowerValue
	}
	return lowerValue + (upperValue-lowerValue)*(position-float64(lower))
}

func virtualPingLossPoint(point Point) Point {
	point.MetricName = sqliteVirtualPingLossMetric
	if point.Value < 0 {
		point.Value = 1
	} else {
		point.Value = 0
	}
	return point
}

func restoreVirtualPingLossPoints(points []Point) []Point {
	for index := range points {
		points[index] = virtualPingLossPoint(points[index])
	}
	return points
}

func restoreVirtualPingLossAggregates(points []AggregatePoint) []AggregatePoint {
	for index := range points {
		points[index].MetricName = sqliteVirtualPingLossMetric
	}
	return points
}
