package metricstore

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/nuomiiiii/lite/pkg/metric"
)

// PingLossStats is the reusable packet-loss aggregate over ping.loss.
type PingLossStats struct {
	Total int64
	Lost  int64
}

func (stats PingLossStats) LossRate() float64 {
	if stats.Total == 0 {
		return 0
	}
	return float64(stats.Lost) / float64(stats.Total) * 100
}

func (stats PingLossStats) LossRatio() float64 {
	if stats.Total == 0 {
		return 0
	}
	return float64(stats.Lost) / float64(stats.Total)
}

// QueryPingLossStats reads existing ping.loss points and does not write any
// metric or application rows.
func QueryPingLossStats(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time) (PingLossStats, error) {
	if store == nil {
		return PingLossStats{}, fmt.Errorf("metric store is not initialized")
	}
	interval := store.CompatibleSeriesIntervalForMetric(ctx, MetricPingLoss, start, end, time.Minute)
	points, err := store.Series(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: MetricPingLoss,
			EntityID:   clientUUID,
			Start:      start,
			End:        end,
			Order:      metric.OrderAsc,
			Tags:       map[string]string{"task_id": fmt.Sprintf("%d", taskID)},
		},
		Aggregation:    metric.AggAvg,
		Interval:       interval,
		PreserveSeries: true,
	}, end)
	if err != nil {
		return PingLossStats{}, err
	}
	return PingLossStatsFromPoints(points), nil
}

func PingLossStatsFromPoints(points []metric.AggregatePoint) PingLossStats {
	var stats PingLossStats
	for _, point := range points {
		if point.Count <= 0 {
			continue
		}
		count := int64(point.Count)
		lost := int64(math.Round(point.Value * float64(point.Count)))
		if lost < 0 {
			lost = 0
		} else if lost > count {
			lost = count
		}
		stats.Total += count
		stats.Lost += lost
	}
	return stats
}
