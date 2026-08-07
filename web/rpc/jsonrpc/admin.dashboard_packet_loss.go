package jsonrpc

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	dbtasks "github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/pkg/metric"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
	publicweb "github.com/komari-monitor/komari/web/public"
)

const dashboardPacketLossWindow = 15 * time.Minute

type dashboardPacketLossRankItem struct {
	UUID        string  `json:"uuid"`
	Name        string  `json:"name"`
	TaskID      uint    `json:"task_id"`
	TaskName    string  `json:"task_name"`
	LossRate    float64 `json:"loss_rate"`
	Lost        int     `json:"lost"`
	Total       int     `json:"total"`
	Valid       int     `json:"valid"`
	DetailURL   string  `json:"detail_url,omitempty"`
	clientOrder int     `json:"-"`
}

type dashboardPacketLossSummary struct {
	WindowMinutes int                           `json:"window_minutes"`
	Ranking       []dashboardPacketLossRankItem `json:"ranking"`
	Error         string                        `json:"error,omitempty"`
}

type dashboardPacketLossAggregate struct {
	losses float64
	total  int
}

type dashboardPacketLossKey struct {
	client string
	taskID uint
}

func loadDashboardPacketLoss(ctx context.Context, clientList []models.Client, now time.Time, rankingLimit int) (dashboardPacketLossSummary, error) {
	result := dashboardPacketLossSummary{WindowMinutes: int(dashboardPacketLossWindow / time.Minute)}
	store := metricstore.GetStore()
	if store == nil {
		return result, fmt.Errorf("metric store is not initialized")
	}
	taskList, err := dbtasks.GetAllPingTasks()
	if err != nil {
		return result, fmt.Errorf("list ping tasks: %w", err)
	}
	if len(taskList) == 0 || len(clientList) == 0 {
		return result, nil
	}

	start := now.Add(-dashboardPacketLossWindow)
	series, err := store.DashboardSeries(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: metricstore.MetricPingLoss,
			Start:      start,
			End:        now,
			Order:      metric.OrderAsc,
		},
		Aggregation:    metric.AggAvg,
		Interval:       dashboardPacketLossWindow,
		PreserveSeries: true,
	}, now)
	if err != nil {
		return result, fmt.Errorf("query fifteen-minute packet-loss window: %w", err)
	}

	online := make(map[string]struct{})
	for _, uuid := range agent_runtime.GetAllOnlineUUIDs() {
		online[uuid] = struct{}{}
	}
	result.Ranking = summarizeDashboardPacketLoss(clientList, taskList, series, online, rankingLimit)
	return result, nil
}

func summarizeDashboardPacketLoss(clientList []models.Client, taskList []models.PingTask, series []metric.AggregatePoint, online map[string]struct{}, rankingLimit int) []dashboardPacketLossRankItem {
	tasksByID := make(map[uint]models.PingTask, len(taskList))
	for _, task := range taskList {
		tasksByID[task.Id] = task
	}
	clientsByID := make(map[string]struct{}, len(clientList))
	for _, client := range clientList {
		clientsByID[client.UUID] = struct{}{}
	}
	aggregates := make(map[dashboardPacketLossKey]dashboardPacketLossAggregate, len(series))
	for _, point := range series {
		if point.Count <= 0 || math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
			continue
		}
		if _, ok := clientsByID[point.EntityID]; !ok {
			continue
		}
		taskID64, parseErr := strconv.ParseUint(strings.TrimSpace(point.Tags["task_id"]), 10, 64)
		if parseErr != nil {
			continue
		}
		taskID := uint(taskID64)
		if _, ok := tasksByID[taskID]; !ok {
			continue
		}
		value := math.Max(0, math.Min(1, point.Value))
		key := dashboardPacketLossKey{client: point.EntityID, taskID: taskID}
		aggregate := aggregates[key]
		aggregate.losses += value * float64(point.Count)
		aggregate.total += point.Count
		aggregates[key] = aggregate
	}

	result := make([]dashboardPacketLossRankItem, 0, rankingLimit)
	for clientOrder, client := range clientList {
		if _, ok := online[client.UUID]; !ok {
			continue
		}
		var best dashboardPacketLossRankItem
		found := false
		for _, task := range taskList {
			if !task.AppliesToClient(client.UUID) {
				continue
			}
			aggregate := aggregates[dashboardPacketLossKey{client: client.UUID, taskID: task.Id}]
			if aggregate.total <= 0 {
				continue
			}
			lost := int(math.Round(aggregate.losses))
			if lost <= 0 {
				continue
			}
			lossRate := aggregate.losses / float64(aggregate.total) * 100
			candidate := dashboardPacketLossRankItem{
				UUID:        client.UUID,
				Name:        dashboardNodeName(client),
				TaskID:      task.Id,
				TaskName:    dashboardTaskName(task),
				LossRate:    lossRate,
				Lost:        lost,
				Total:       aggregate.total,
				Valid:       max(0, aggregate.total-lost),
				clientOrder: clientOrder,
			}
			if !found || dashboardPacketLossBefore(candidate, best) {
				best = candidate
				found = true
			}
		}
		if found {
			result = dashboardTopPacketLoss(result, best, rankingLimit)
		}
	}
	return result
}

func dashboardPacketLossBefore(left, right dashboardPacketLossRankItem) bool {
	if left.LossRate != right.LossRate {
		return left.LossRate > right.LossRate
	}
	if left.Lost != right.Lost {
		return left.Lost > right.Lost
	}
	if left.Valid != right.Valid {
		return left.Valid > right.Valid
	}
	return left.clientOrder < right.clientOrder
}

func dashboardTopPacketLoss(top []dashboardPacketLossRankItem, item dashboardPacketLossRankItem, limit int) []dashboardPacketLossRankItem {
	if !dashboardRankingLimitAllowed(limit) {
		limit = 5
	}
	insertAt := len(top)
	for index, current := range top {
		if dashboardPacketLossBefore(item, current) {
			insertAt = index
			break
		}
	}
	if insertAt >= limit {
		return top
	}
	if len(top) < limit {
		top = append(top, dashboardPacketLossRankItem{})
	}
	copy(top[insertAt+1:], top[insertAt:len(top)-1])
	top[insertAt] = item
	return top
}

func dashboardNodeName(client models.Client) string {
	if name := strings.TrimSpace(client.Name); name != "" {
		return name
	}
	return client.UUID
}

func dashboardTaskName(task models.PingTask) string {
	if name := strings.TrimSpace(task.Name); name != "" {
		return name
	}
	return fmt.Sprintf("#%d", task.Id)
}

func decorateDashboardNavigation(result dashboardChartsResponse) dashboardChartsResponse {
	navigation := publicweb.ActiveThemeNavigation()
	result.Traffic.Ranking = append([]dashboardTrafficRankItem(nil), result.Traffic.Ranking...)
	for index := range result.Traffic.Ranking {
		result.Traffic.Ranking[index].DetailURL = navigation.ServerDetailURL(result.Traffic.Ranking[index].UUID, 0)
	}
	result.Latency.JitterRanking = append([]dashboardLatencyJitterRankItem(nil), result.Latency.JitterRanking...)
	result.Latency.Ranking = append([]dashboardLatencyRankItem(nil), result.Latency.Ranking...)
	for index := range result.Latency.Ranking {
		item := &result.Latency.Ranking[index]
		item.DetailURL = navigation.ServerNetworkURL(item.UUID)
	}
	for index := range result.Latency.JitterRanking {
		item := &result.Latency.JitterRanking[index]
		item.DetailURL = navigation.ServerNetworkURL(item.UUID)
	}
	result.PacketLoss.Ranking = append([]dashboardPacketLossRankItem(nil), result.PacketLoss.Ranking...)
	for index := range result.PacketLoss.Ranking {
		item := &result.PacketLoss.Ranking[index]
		item.DetailURL = navigation.ServerDetailURL(item.UUID, item.TaskID)
	}
	return result
}

func decorateDashboardSummaryNavigation(result dashboardResponse) dashboardResponse {
	navigation := publicweb.ActiveThemeNavigation()
	decorate := func(items []dashboardResourceRankItem) []dashboardResourceRankItem {
		items = append([]dashboardResourceRankItem(nil), items...)
		for index := range items {
			items[index].DetailURL = navigation.ServerDetailURL(items[index].UUID, 0)
		}
		return items
	}
	result.Resources.CPU = decorate(result.Resources.CPU)
	result.Resources.Memory = decorate(result.Resources.Memory)
	result.Resources.Disk = decorate(result.Resources.Disk)
	return result
}
