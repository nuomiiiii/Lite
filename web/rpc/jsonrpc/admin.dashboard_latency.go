package jsonrpc

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	dbtasks "github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/pkg/metric"
)

type dashboardLatencyPoint struct {
	Time    time.Time `json:"time"`
	Average float64   `json:"average"`
}

type dashboardLatencySummary struct {
	Average float64                 `json:"average"`
	Targets int                     `json:"targets"`
	Points  []dashboardLatencyPoint `json:"points"`
	Error   string                  `json:"error,omitempty"`
}

type dashboardLatencyBucket struct {
	Sum   float64
	Count int
}

func loadDashboardLatency(ctx context.Context, clientList []models.Client, now time.Time) (dashboardLatencySummary, error) {
	result := dashboardLatencySummary{}
	if pingTasks, err := dbtasks.GetAllPingTasks(); err == nil {
		result.Targets = len(pingTasks)
	}
	store := metricstore.GetStore()
	if store == nil {
		return result, fmt.Errorf("metric store is not initialized")
	}
	start := now.Add(-6 * time.Hour)
	interval := store.CompatibleSeriesInterval(start, now, time.Hour)
	buckets := make(map[time.Time]dashboardLatencyBucket)
	failed := 0
	for _, client := range clientList {
		series, err := store.PingSeriesSummary(ctx, metric.AggregateQuery{
			Query: metric.Query{
				MetricName: metricstore.MetricPingLatency,
				EntityID:   client.UUID,
				Start:      start,
				End:        now,
				Order:      metric.OrderAsc,
			},
			Aggregation: metric.AggAvg,
			Interval:    interval,
		}, now)
		if err != nil {
			failed++
			continue
		}
		for _, point := range series.Avg {
			if point.Count <= 0 || point.Value < 0 {
				continue
			}
			bucketTime := point.Bucket.UTC()
			bucket := buckets[bucketTime]
			bucket.Sum += point.Value * float64(point.Count)
			bucket.Count += point.Count
			buckets[bucketTime] = bucket
		}
	}

	times := make([]time.Time, 0, len(buckets))
	for bucketTime := range buckets {
		times = append(times, bucketTime)
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	result.Points = make([]dashboardLatencyPoint, 0, len(times))
	var total float64
	var count int
	for _, bucketTime := range times {
		bucket := buckets[bucketTime]
		if bucket.Count <= 0 {
			continue
		}
		average := bucket.Sum / float64(bucket.Count)
		result.Points = append(result.Points, dashboardLatencyPoint{Time: bucketTime, Average: average})
		total += bucket.Sum
		count += bucket.Count
	}
	if count > 0 {
		result.Average = total / float64(count)
	}
	if failed > 0 && failed == len(clientList) {
		return result, fmt.Errorf("latency data unavailable for %d nodes", failed)
	}
	return result, nil
}
