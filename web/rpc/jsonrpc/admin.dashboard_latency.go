package jsonrpc

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/metric"
)

type dashboardLatencyPoint struct {
	Time    time.Time `json:"time"`
	Average float64   `json:"average"`
}

type dashboardLatencySummary struct {
	Average       float64                          `json:"average"`
	Targets       int                              `json:"targets"`
	Points        []dashboardLatencyPoint          `json:"points"`
	Ranking       []dashboardLatencyRankItem       `json:"ranking"`
	JitterRanking []dashboardLatencyJitterRankItem `json:"jitter_ranking"`
	JitterError   string                           `json:"jitter_error,omitempty"`
	Error         string                           `json:"error,omitempty"`
}

type dashboardLatencyRankItem struct {
	UUID        string  `json:"uuid"`
	Name        string  `json:"name"`
	Average     float64 `json:"average"`
	TaskID      uint    `json:"task_id,omitempty"`
	TaskName    string  `json:"task_name,omitempty"`
	DetailURL   string  `json:"detail_url,omitempty"`
	clientOrder int     `json:"-"`
}

type dashboardLatencyJitterRankItem struct {
	UUID        string  `json:"uuid"`
	Name        string  `json:"name"`
	Previous    float64 `json:"previous"`
	Current     float64 `json:"current"`
	Delta       float64 `json:"delta"`
	TaskID      uint    `json:"task_id,omitempty"`
	TaskName    string  `json:"task_name,omitempty"`
	DetailURL   string  `json:"detail_url,omitempty"`
	clientOrder int     `json:"-"`
}

type dashboardLatencyBucket struct {
	Sum   float64
	Count int
}

const dashboardLatencyJitterLookback = 10 * time.Minute

func loadDashboardLatency(ctx context.Context, clientList []models.Client, pingTasks []models.PingTask, now time.Time, rankingLimit int) (dashboardLatencySummary, error) {
	result := dashboardLatencySummary{Targets: len(pingTasks)}
	store := metricstore.GetStore()
	if store == nil {
		return result, fmt.Errorf("metric store is not initialized")
	}
	start := now.Add(-6 * time.Hour)
	interval := store.CompatibleSeriesIntervalForMetric(ctx, metricstore.MetricPingLatency, start, now, time.Hour)
	series, err := store.DashboardSeries(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: metricstore.MetricPingLatency,
			Start:      start,
			End:        now,
			Order:      metric.OrderAsc,
		},
		Aggregation:    metric.AggAvg,
		Interval:       interval,
		PreserveSeries: true,
	}, now)
	if err != nil {
		return result, fmt.Errorf("query six-hour latency window: %w", err)
	}

	clientsByID := make(map[string]models.Client, len(clientList))
	for _, client := range clientList {
		clientsByID[client.UUID] = client
	}
	buckets := make(map[time.Time]dashboardLatencyBucket)
	for _, point := range series {
		if point.Count <= 0 || point.Value < 0 {
			continue
		}
		if _, ok := clientsByID[point.EntityID]; !ok {
			continue
		}
		weighted := point.Value * float64(point.Count)
		bucketTime := point.Bucket.UTC()
		bucket := buckets[bucketTime]
		bucket.Sum += weighted
		bucket.Count += point.Count
		buckets[bucketTime] = bucket
	}
	result.Ranking = summarizeDashboardLatencyRanking(clientList, pingTasks, series, rankingLimit)

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
	return result, nil
}

func summarizeDashboardLatencyRanking(clientList []models.Client, taskList []models.PingTask, series []metric.AggregatePoint, rankingLimit int) []dashboardLatencyRankItem {
	tasksByID := make(map[uint]models.PingTask, len(taskList))
	for _, task := range taskList {
		tasksByID[task.Id] = task
	}
	aggregates := make(map[dashboardLatencyJitterKey]dashboardLatencyBucket, len(series))
	for _, point := range series {
		if point.Count <= 0 || point.Value < 0 {
			continue
		}
		taskID, ok := dashboardLatencyTaskID(point)
		if !ok {
			continue
		}
		if _, ok := tasksByID[taskID]; !ok {
			continue
		}
		key := dashboardLatencyJitterKey{client: point.EntityID, taskID: taskID}
		bucket := aggregates[key]
		bucket.Sum += point.Value * float64(point.Count)
		bucket.Count += point.Count
		aggregates[key] = bucket
	}
	result := make([]dashboardLatencyRankItem, 0, rankingLimit)
	for clientOrder, client := range clientList {
		for _, task := range taskList {
			if !task.AppliesToClient(client.UUID) {
				continue
			}
			bucket := aggregates[dashboardLatencyJitterKey{client: client.UUID, taskID: task.Id}]
			if bucket.Count <= 0 {
				continue
			}
			result = dashboardTopLatency(result, dashboardLatencyRankItem{
				UUID:        client.UUID,
				Name:        dashboardNodeName(client),
				Average:     bucket.Sum / float64(bucket.Count),
				TaskID:      task.Id,
				TaskName:    dashboardTaskName(task),
				clientOrder: clientOrder,
			}, rankingLimit)
		}
	}
	return result
}

func dashboardLatencyRankBefore(left, right dashboardLatencyRankItem) bool {
	if left.Average != right.Average {
		return left.Average > right.Average
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.TaskName != right.TaskName {
		return left.TaskName < right.TaskName
	}
	return left.clientOrder < right.clientOrder
}

func dashboardTopLatency(top []dashboardLatencyRankItem, item dashboardLatencyRankItem, limit int) []dashboardLatencyRankItem {
	return dashboardInsertRanked(top, item, limit, dashboardLatencyRankBefore)
}

func loadDashboardLatencyJitter(ctx context.Context, clientList []models.Client, pingTasks []models.PingTask, now time.Time, rankingLimit int) ([]dashboardLatencyJitterRankItem, error) {
	store := metricstore.GetStore()
	if store == nil {
		return nil, fmt.Errorf("metric store is not initialized")
	}
	if len(pingTasks) == 0 || len(clientList) == 0 {
		return []dashboardLatencyJitterRankItem{}, nil
	}
	currentMinute := now.UTC().Truncate(time.Minute)
	series, err := store.DashboardSeries(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: metricstore.MetricPingLatency,
			Start:      currentMinute.Add(-dashboardLatencyJitterLookback),
			End:        now,
			Order:      metric.OrderAsc,
		},
		Aggregation:    metric.AggAvg,
		Interval:       time.Minute,
		PreserveSeries: true,
	}, now)
	if err != nil {
		return nil, fmt.Errorf("query latency jitter window: %w", err)
	}
	return summarizeDashboardLatencyJitter(clientList, pingTasks, series, currentMinute, rankingLimit), nil
}

type dashboardLatencyJitterKey struct {
	client string
	taskID uint
}

func summarizeDashboardLatencyJitter(clientList []models.Client, taskList []models.PingTask, series []metric.AggregatePoint, currentMinute time.Time, rankingLimit int) []dashboardLatencyJitterRankItem {
	tasksByID := make(map[uint]models.PingTask, len(taskList))
	for _, task := range taskList {
		tasksByID[task.Id] = task
	}
	pointsBySeries := make(map[dashboardLatencyJitterKey][]metric.AggregatePoint, len(series))
	for _, point := range series {
		taskID, ok := dashboardLatencyTaskID(point)
		if !ok {
			continue
		}
		if _, ok := tasksByID[taskID]; !ok {
			continue
		}
		key := dashboardLatencyJitterKey{client: point.EntityID, taskID: taskID}
		pointsBySeries[key] = append(pointsBySeries[key], point)
	}
	result := make([]dashboardLatencyJitterRankItem, 0, rankingLimit)
	for clientOrder, client := range clientList {
		for _, task := range taskList {
			if !task.AppliesToClient(client.UUID) {
				continue
			}
			previous, current, ok := dashboardLatestLatencyMinuteAverages(pointsBySeries[dashboardLatencyJitterKey{client: client.UUID, taskID: task.Id}], currentMinute)
			if !ok {
				continue
			}
			result = dashboardTopLatencyJitter(result, dashboardLatencyJitterRankItem{
				UUID:        client.UUID,
				Name:        dashboardNodeName(client),
				Previous:    previous,
				Current:     current,
				Delta:       current - previous,
				TaskID:      task.Id,
				TaskName:    dashboardTaskName(task),
				clientOrder: clientOrder,
			}, rankingLimit)
		}
	}
	return result
}

func dashboardLatencyTaskID(point metric.AggregatePoint) (uint, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(point.Tags["task_id"]), 10, 64)
	return uint(value), err == nil && value > 0
}

func dashboardLatestLatencyMinuteAverages(points []metric.AggregatePoint, currentMinute time.Time) (float64, float64, bool) {
	buckets := dashboardLatencyMinuteBuckets(points)
	for later := currentMinute; !later.Before(currentMinute.Add(-dashboardLatencyJitterLookback)); later = later.Add(-time.Minute) {
		earlier := later.Add(-time.Minute)
		previous, previousOK := buckets[earlier]
		current, currentOK := buckets[later]
		if !previousOK || !currentOK || previous.Count == 0 || current.Count == 0 {
			continue
		}
		return previous.Sum / float64(previous.Count), current.Sum / float64(current.Count), true
	}
	return 0, 0, false
}

func dashboardLatencyMinuteBuckets(points []metric.AggregatePoint) map[time.Time]dashboardLatencyBucket {
	buckets := make(map[time.Time]dashboardLatencyBucket)
	for _, point := range points {
		if point.Count <= 0 || point.Value < 0 {
			continue
		}
		bucket := point.Bucket.UTC().Truncate(time.Minute)
		value := buckets[bucket]
		value.Sum += point.Value * float64(point.Count)
		value.Count += point.Count
		buckets[bucket] = value
	}
	return buckets
}

func dashboardLatencyJitterBefore(left, right dashboardLatencyJitterRankItem) bool {
	if left.Delta != right.Delta {
		return left.Delta > right.Delta
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.TaskName != right.TaskName {
		return left.TaskName < right.TaskName
	}
	return left.clientOrder < right.clientOrder
}

func dashboardTopLatencyJitter(top []dashboardLatencyJitterRankItem, item dashboardLatencyJitterRankItem, limit int) []dashboardLatencyJitterRankItem {
	return dashboardInsertRanked(top, item, limit, dashboardLatencyJitterBefore)
}
