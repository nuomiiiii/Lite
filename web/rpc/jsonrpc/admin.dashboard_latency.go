package jsonrpc

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	Average       float64                          `json:"average"`
	Targets       int                              `json:"targets"`
	Points        []dashboardLatencyPoint          `json:"points"`
	Ranking       []dashboardLatencyRankItem       `json:"ranking"`
	JitterRanking []dashboardLatencyJitterRankItem `json:"jitter_ranking"`
	JitterError   string                           `json:"jitter_error,omitempty"`
	Error         string                           `json:"error,omitempty"`
}

type dashboardLatencyRankItem struct {
	UUID      string  `json:"uuid"`
	Name      string  `json:"name"`
	Average   float64 `json:"average"`
	TaskID    uint    `json:"task_id,omitempty"`
	DetailURL string  `json:"detail_url,omitempty"`
}

type dashboardLatencyJitterRankItem struct {
	UUID      string  `json:"uuid"`
	Name      string  `json:"name"`
	Previous  float64 `json:"previous"`
	Current   float64 `json:"current"`
	Delta     float64 `json:"delta"`
	TaskID    uint    `json:"task_id,omitempty"`
	DetailURL string  `json:"detail_url,omitempty"`
}

type dashboardLatencyBucket struct {
	Sum   float64
	Count int
}

func loadDashboardLatency(ctx context.Context, clientList []models.Client, now time.Time, rankingLimit int) (dashboardLatencySummary, error) {
	result := dashboardLatencySummary{}
	pingTasks, pingTasksErr := dbtasks.GetAllPingTasks()
	if pingTasksErr == nil {
		result.Targets = len(pingTasks)
	}
	store := metricstore.GetStore()
	if store == nil {
		return result, fmt.Errorf("metric store is not initialized")
	}
	start := now.Add(-6 * time.Hour)
	interval := store.CompatibleSeriesInterval(start, now, time.Hour)
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
	nodeBuckets := make(map[string]dashboardLatencyBucket, len(clientList))
	nodeTaskIDs := make(map[string]map[uint]struct{}, len(clientList))
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
		node := nodeBuckets[point.EntityID]
		node.Sum += weighted
		node.Count += point.Count
		nodeBuckets[point.EntityID] = node
		if taskID, ok := dashboardLatencyTaskID(point); ok {
			if nodeTaskIDs[point.EntityID] == nil {
				nodeTaskIDs[point.EntityID] = make(map[uint]struct{})
			}
			nodeTaskIDs[point.EntityID][taskID] = struct{}{}
		}
	}
	for _, client := range clientList {
		node := nodeBuckets[client.UUID]
		if node.Count <= 0 {
			continue
		}
		name := strings.TrimSpace(client.Name)
		if name == "" {
			name = client.UUID
		}
		result.Ranking = dashboardTopLatency(result.Ranking, dashboardLatencyRankItem{
			UUID: client.UUID, Name: name, Average: node.Sum / float64(node.Count),
			TaskID: dashboardPreferredPingTaskID(client.UUID, nodeTaskIDs[client.UUID], pingTasks),
		}, rankingLimit)
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
	return result, nil
}

func dashboardTopLatency(top []dashboardLatencyRankItem, item dashboardLatencyRankItem, limit int) []dashboardLatencyRankItem {
	if !dashboardRankingLimitAllowed(limit) {
		limit = 5
	}
	insertAt := len(top)
	for index, current := range top {
		if item.Average > current.Average || (item.Average == current.Average && item.Name < current.Name) {
			insertAt = index
			break
		}
	}
	if insertAt >= limit {
		return top
	}
	if len(top) < limit {
		top = append(top, dashboardLatencyRankItem{})
	}
	copy(top[insertAt+1:], top[insertAt:len(top)-1])
	top[insertAt] = item
	return top
}

func loadDashboardLatencyJitter(ctx context.Context, clientList []models.Client, now time.Time, rankingLimit int) ([]dashboardLatencyJitterRankItem, error) {
	store := metricstore.GetStore()
	if store == nil {
		return nil, fmt.Errorf("metric store is not initialized")
	}
	currentMinute := now.UTC().Truncate(time.Minute)
	previousMinute := currentMinute.Add(-time.Minute)
	series, err := store.DashboardSeries(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: metricstore.MetricPingLatency,
			Start:      previousMinute,
			End:        now,
			Order:      metric.OrderAsc,
		},
		Aggregation:    metric.AggAvg,
		Interval:       time.Minute,
		PreserveSeries: true,
	}, now)
	if err != nil {
		return nil, fmt.Errorf("query two-minute latency window: %w", err)
	}
	pingTasks, _ := dbtasks.GetAllPingTasks()

	pointsByClient := make(map[string][]metric.AggregatePoint, len(clientList))
	nodeTaskIDs := make(map[string]map[uint]struct{}, len(clientList))
	for _, point := range series {
		pointsByClient[point.EntityID] = append(pointsByClient[point.EntityID], point)
		if point.Count > 0 && point.Value >= 0 {
			if taskID, ok := dashboardLatencyTaskID(point); ok {
				if nodeTaskIDs[point.EntityID] == nil {
					nodeTaskIDs[point.EntityID] = make(map[uint]struct{})
				}
				nodeTaskIDs[point.EntityID][taskID] = struct{}{}
			}
		}
	}
	result := make([]dashboardLatencyJitterRankItem, 0, rankingLimit)
	for _, client := range clientList {
		points := pointsByClient[client.UUID]
		previous, current, ok := dashboardLatencyMinuteAverages(points, previousMinute, currentMinute)
		if !ok {
			continue
		}
		name := strings.TrimSpace(client.Name)
		if name == "" {
			name = client.UUID
		}
		result = dashboardTopLatencyJitter(result, dashboardLatencyJitterRankItem{
			UUID: client.UUID, Name: name, Previous: previous, Current: current, Delta: current - previous,
			TaskID: dashboardPreferredPingTaskID(client.UUID, nodeTaskIDs[client.UUID], pingTasks),
		}, rankingLimit)
	}
	return result, nil
}

func dashboardLatencyTaskID(point metric.AggregatePoint) (uint, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(point.Tags["task_id"]), 10, 64)
	return uint(value), err == nil && value > 0
}

func dashboardPreferredPingTaskID(clientUUID string, validTaskIDs map[uint]struct{}, tasks []models.PingTask) uint {
	for _, task := range tasks {
		if _, ok := validTaskIDs[task.Id]; ok && task.AppliesToClient(clientUUID) {
			return task.Id
		}
	}
	return 0
}

func dashboardLatencyMinuteAverages(points []metric.AggregatePoint, previousMinute, currentMinute time.Time) (float64, float64, bool) {
	var previousSum, currentSum float64
	var previousCount, currentCount int
	for _, point := range points {
		if point.Count <= 0 || point.Value < 0 {
			continue
		}
		bucket := point.Bucket.UTC().Truncate(time.Minute)
		switch bucket {
		case previousMinute:
			previousSum += point.Value * float64(point.Count)
			previousCount += point.Count
		case currentMinute:
			currentSum += point.Value * float64(point.Count)
			currentCount += point.Count
		}
	}
	if previousCount == 0 || currentCount == 0 {
		return 0, 0, false
	}
	return previousSum / float64(previousCount), currentSum / float64(currentCount), true
}

func dashboardTopLatencyJitter(top []dashboardLatencyJitterRankItem, item dashboardLatencyJitterRankItem, limit int) []dashboardLatencyJitterRankItem {
	if !dashboardRankingLimitAllowed(limit) {
		limit = 5
	}
	insertAt := len(top)
	for index, current := range top {
		if item.Delta > current.Delta || (item.Delta == current.Delta && item.Name < current.Name) {
			insertAt = index
			break
		}
	}
	if insertAt >= limit {
		return top
	}
	if len(top) < limit {
		top = append(top, dashboardLatencyJitterRankItem{})
	}
	copy(top[insertAt+1:], top[insertAt:len(top)-1])
	top[insertAt] = item
	return top
}
