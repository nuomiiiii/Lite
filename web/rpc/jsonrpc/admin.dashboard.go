package jsonrpc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"github.com/nuomiiiii/lite/pkg/rpc"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

type dashboardOfflineNode struct {
	UUID     string     `json:"uuid"`
	Name     string     `json:"name"`
	Region   string     `json:"region"`
	LastSeen *time.Time `json:"last_seen"`
}

type dashboardServerSummary struct {
	Total        int                    `json:"total"`
	Online       int                    `json:"online"`
	Offline      int                    `json:"offline"`
	OfflineNodes []dashboardOfflineNode `json:"offline_nodes"`
}

type dashboardTrafficDay struct {
	Day      string `json:"day"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
	Billable int64  `json:"billable"`
}

type dashboardTrafficHour struct {
	Hour string `json:"hour"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

type dashboardTrafficRankItem struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	IPv4      string `json:"ipv4,omitempty"`
	IPv6      string `json:"ipv6,omitempty"`
	Group     string `json:"group,omitempty"`
	Tags      string `json:"tags,omitempty"`
	Up        int64  `json:"up"`
	Down      int64  `json:"down"`
	Billable  int64  `json:"billable"`
	DetailURL string `json:"detail_url,omitempty"`
}

type dashboardTrafficSummary struct {
	TodayUp              int64                      `json:"today_up"`
	TodayDown            int64                      `json:"today_down"`
	TodayBillable        int64                      `json:"today_billable"`
	PeriodUp             int64                      `json:"period_up"`
	PeriodDown           int64                      `json:"period_down"`
	PeriodBillable       int64                      `json:"period_billable"`
	PeriodDays           int                        `json:"period_days"`
	DailyAverageUp       int64                      `json:"daily_average_up"`
	DailyAverageDown     int64                      `json:"daily_average_down"`
	DailyAverageBillable int64                      `json:"daily_average_billable"`
	Hourly               []dashboardTrafficHour     `json:"hourly"`
	Daily                []dashboardTrafficDay      `json:"daily"`
	Ranking              []dashboardTrafficRankItem `json:"ranking"`
	HistoryReady         bool                       `json:"history_ready"`
	Error                string                     `json:"error,omitempty"`
}

type dashboardTrafficDayResponse struct {
	Day         string                     `json:"day"`
	Items       []dashboardTrafficRankItem `json:"items"`
	GeneratedAt time.Time                  `json:"generated_at"`
}

type dashboardStorageSummary struct {
	DatabaseFiles   int64      `json:"database_files"`
	WAL             int64      `json:"wal"`
	SHM             int64      `json:"shm"`
	RetentionDays   int        `json:"retention_days"`
	LastCompactedAt *time.Time `json:"last_compacted_at"`
}

type dashboardReturnRouteSummary struct {
	tasks.ReturnRouteSummary
	LatestEvent *tasks.ReturnRouteEventItem `json:"latest_event,omitempty"`
	Error       string                      `json:"error,omitempty"`
}

type dashboardResourceRankItem struct {
	UUID      string  `json:"uuid"`
	Name      string  `json:"name"`
	CPU       float64 `json:"cpu"`
	Memory    float64 `json:"memory"`
	Disk      float64 `json:"disk"`
	DetailURL string  `json:"detail_url,omitempty"`
}

type dashboardResourceSummary struct {
	CPU    []dashboardResourceRankItem `json:"cpu"`
	Memory []dashboardResourceRankItem `json:"memory"`
	Disk   []dashboardResourceRankItem `json:"disk"`
}

type dashboardResponse struct {
	Servers     dashboardServerSummary      `json:"servers"`
	Resources   dashboardResourceSummary    `json:"resources"`
	Database    databaseStatusResponse      `json:"database"`
	Storage     dashboardStorageSummary     `json:"storage"`
	ReturnRoute dashboardReturnRouteSummary `json:"return_route"`
	Alerts      dashboardAlertSummaries     `json:"alerts"`
	GeneratedAt time.Time                   `json:"generated_at"`
}

type dashboardSummarySections uint8

const (
	dashboardSectionServers dashboardSummarySections = 1 << iota
	dashboardSectionResources
	dashboardSectionStorage
	dashboardSectionReturnRoute
	dashboardSectionAlerts
	dashboardSectionAll = dashboardSectionServers | dashboardSectionResources |
		dashboardSectionStorage | dashboardSectionReturnRoute | dashboardSectionAlerts
)

type dashboardChartSections uint8

const (
	dashboardChartTraffic dashboardChartSections = 1 << iota
	dashboardChartLatency
	dashboardChartLatencyJitter
	dashboardChartPacketLoss
	dashboardChartAll = dashboardChartTraffic | dashboardChartLatency | dashboardChartLatencyJitter | dashboardChartPacketLoss
)

type dashboardChartsResponse struct {
	Traffic     dashboardTrafficSummary    `json:"traffic"`
	Latency     dashboardLatencySummary    `json:"latency"`
	PacketLoss  dashboardPacketLossSummary `json:"packet_loss"`
	GeneratedAt time.Time                  `json:"generated_at"`
}

func init() {
	RegisterWithGroupAndMeta("getDashboard", rpc.RoleAdmin, adminGetDashboard, &rpc.MethodMeta{
		Name:    "admin:getDashboard",
		Summary: "Get the cached administration dashboard summary",
		Returns: "DashboardSummary",
	})
	RegisterWithGroupAndMeta("getDashboardCharts", rpc.RoleAdmin, adminGetDashboardCharts, &rpc.MethodMeta{
		Name:    "admin:getDashboardCharts",
		Summary: "Get cached administration dashboard chart data",
		Returns: "DashboardCharts",
	})
	RegisterWithGroupAndMeta("getDashboardAlertItems", rpc.RoleAdmin, adminGetDashboardAlertItems, &rpc.MethodMeta{
		Name:    "admin:getDashboardAlertItems",
		Summary: "List currently affected dashboard alert targets",
		Returns: "DashboardAlertItems",
	})
	RegisterWithGroupAndMeta("getClientTrafficDaily", rpc.RoleAdmin, adminGetClientTrafficDaily, &rpc.MethodMeta{
		Name:    "admin:getClientTrafficDaily",
		Summary: "Get one client's dashboard daily traffic ledger",
		Returns: "ClientTrafficDaily",
	})
	RegisterWithGroupAndMeta("getDashboardTrafficDay", rpc.RoleAdmin, adminGetDashboardTrafficDay, &rpc.MethodMeta{
		Name:    "admin:getDashboardTrafficDay",
		Summary: "List per-server traffic for one dashboard day",
		Returns: "DashboardTrafficDay",
	})
}

func adminGetDashboard(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	now := time.Now().UTC()
	sections, rankingLimit := parseDashboardSummaryRequest(req)
	settings := loadDashboardSettings()
	value, err := buildDashboardCached(ctx, now, sections, rankingLimit, time.Duration(settings.RefreshSeconds)*time.Second)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	return decorateDashboardSummaryNavigation(value), nil
}

func adminGetDashboardCharts(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	now := time.Now().UTC()
	sections, rankingLimit := parseDashboardChartRequest(req)
	settings := loadDashboardSettings()
	return decorateDashboardNavigation(buildDashboardChartsCached(ctx, now, sections, rankingLimit, time.Duration(settings.ChartRefreshSeconds)*time.Second)), nil
}

func adminGetClientTrafficDaily(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID string `json:"uuid"`
	}
	req.BindParams(&params)
	if params.UUID == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "UUID is required", nil)
	}
	list, err := clients.GetClientBasicInfoByUUIDs([]string{params.UUID})
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	if len(list) == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "client not found", nil)
	}
	client := list[0]
	summary, err := loadDashboardTraffic(ctx, list, time.Now().UTC(), 1)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	return map[string]any{
		"daily":              summary.Daily,
		"today_up":           summary.TodayUp,
		"today_down":         summary.TodayDown,
		"today_billable":     summary.TodayBillable,
		"history_ready":      summary.HistoryReady,
		"traffic_limit_type": client.TrafficLimitType,
	}, nil
}

func dashboardChartNeedsPingTasks(sections dashboardChartSections) bool {
	return sections&(dashboardChartLatency|dashboardChartLatencyJitter|dashboardChartPacketLoss) != 0
}

func loadDashboardPingTasks(sections dashboardChartSections) ([]models.PingTask, error) {
	if !dashboardChartNeedsPingTasks(sections) {
		return nil, nil
	}
	return tasks.GetAllPingTasks()
}

func parseDashboardSummaryRequest(req *rpc.JsonRpcRequest) (dashboardSummarySections, int) {
	rawSections, _ := rpc.GetParamAs[string](req, "sections")
	sections := dashboardSummarySections(0)
	if strings.TrimSpace(rawSections) == "" {
		sections = dashboardSectionAll
	} else {
		for _, raw := range strings.Split(rawSections, ",") {
			switch strings.TrimSpace(raw) {
			case "servers":
				sections |= dashboardSectionServers
			case "resources":
				sections |= dashboardSectionResources
			case "storage":
				sections |= dashboardSectionStorage
			case "return_route":
				sections |= dashboardSectionReturnRoute
			case "alerts":
				sections |= dashboardSectionAlerts
			}
		}
	}

	return sections, parseDashboardRankingLimit(req)
}

func parseDashboardChartRequest(req *rpc.JsonRpcRequest) (dashboardChartSections, int) {
	rawSections, _ := rpc.GetParamAs[string](req, "sections")
	if strings.TrimSpace(rawSections) == "" {
		return dashboardChartAll, parseDashboardRankingLimit(req)
	}
	sections := dashboardChartSections(0)
	for _, raw := range strings.Split(rawSections, ",") {
		switch strings.TrimSpace(raw) {
		case "traffic":
			sections |= dashboardChartTraffic
		case "latency":
			sections |= dashboardChartLatency
		case "latency_jitter":
			sections |= dashboardChartLatencyJitter
		case "packet_loss":
			sections |= dashboardChartPacketLoss
		}
	}
	return sections, parseDashboardRankingLimit(req)
}

func parseDashboardRankingLimit(req *rpc.JsonRpcRequest) int {
	if rawLimit, ok := rpc.GetParamAs[string](req, "limit"); ok {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			if parsed == dashboardRankingLimitAll || dashboardRankingLimitAllowed(parsed) {
				return parsed
			}
		}
	}
	return 5
}

const dashboardRankingLimitAll = 0

func dashboardNormalizeRankingLimit(limit int) int {
	if limit == dashboardRankingLimitAll || dashboardRankingLimitAllowed(limit) {
		return limit
	}
	return 5
}

func dashboardInsertRanked[T any](top []T, item T, limit int, better func(item, current T) bool) []T {
	limit = dashboardNormalizeRankingLimit(limit)
	insertAt := len(top)
	for index, current := range top {
		if better(item, current) {
			insertAt = index
			break
		}
	}
	if limit > 0 && insertAt >= limit {
		return top
	}
	if limit == 0 || len(top) < limit {
		var zero T
		top = append(top, zero)
	}
	copy(top[insertAt+1:], top[insertAt:len(top)-1])
	top[insertAt] = item
	return top
}

func buildDashboardResources(clientList []models.Client, limit int) dashboardResourceSummary {
	limit = dashboardNormalizeRankingLimit(limit)
	reports := agent_runtime.GetLatestReport()
	items := make([]dashboardResourceRankItem, 0, len(clientList))
	for _, client := range clientList {
		report := reports[client.UUID]
		if report == nil {
			continue
		}
		name := strings.TrimSpace(client.Name)
		if name == "" {
			name = client.UUID
		}
		items = append(items, dashboardResourceRankItem{
			UUID:   client.UUID,
			Name:   name,
			CPU:    dashboardPercent(report.CPU.Usage),
			Memory: dashboardUsagePercent(report.Ram.Used, report.Ram.Total),
			Disk:   dashboardUsagePercent(report.Disk.Used, report.Disk.Total),
		})
	}
	return dashboardResourceSummary{
		CPU:    dashboardTopResources(items, limit, func(item dashboardResourceRankItem) float64 { return item.CPU }),
		Memory: dashboardTopResources(items, limit, func(item dashboardResourceRankItem) float64 { return item.Memory }),
		Disk:   dashboardTopResources(items, limit, func(item dashboardResourceRankItem) float64 { return item.Disk }),
	}
}

func dashboardUsagePercent(used, total int64) float64 {
	if used <= 0 || total <= 0 {
		return 0
	}
	return dashboardPercent(float64(used) / float64(total) * 100)
}

func dashboardPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return math.Min(100, value)
}

// dashboardTopResources keeps only a bounded Top-N list while scanning reports.
// With at most 20 entries this avoids three full sorts and large temporary slices.
func dashboardTopResources(
	items []dashboardResourceRankItem,
	limit int,
	value func(dashboardResourceRankItem) float64,
) []dashboardResourceRankItem {
	capacity := limit
	if capacity <= 0 {
		capacity = len(items)
	}
	top := make([]dashboardResourceRankItem, 0, capacity)
	for _, item := range items {
		top = dashboardInsertRanked(top, item, limit, func(candidate, current dashboardResourceRankItem) bool {
			return value(candidate) > value(current) || (value(candidate) == value(current) && candidate.Name < current.Name)
		})
	}
	return top
}

func buildDashboardReturnRoute() dashboardReturnRouteSummary {
	result := dashboardReturnRouteSummary{}
	summary, err := tasks.GetReturnRouteSummary()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.ReturnRouteSummary = summary

	events, err := tasks.QueryReturnRouteEvents(tasks.ReturnRouteEventQuery{Page: 1, PageSize: 1})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(events.Events) > 0 {
		latest := events.Events[0]
		result.LatestEvent = &latest
	}
	return result
}

func buildDashboardStorage(ctx context.Context, statuses ...databaseStorageStatus) dashboardStorageSummary {
	summary := dashboardStorageSummary{}
	for _, status := range statuses {
		if status.Location != databaseLocationLocal || status.Files == nil {
			continue
		}
		summary.DatabaseFiles += status.Files.Database
		summary.WAL += status.Files.WAL
		summary.SHM += status.Files.SHM
		if status.Runtime != nil && status.Runtime.LastCycleCompletedAt != nil {
			completed := status.Runtime.LastCycleCompletedAt.UTC()
			if summary.LastCompactedAt == nil || completed.After(*summary.LastCompactedAt) {
				summary.LastCompactedAt = &completed
			}
		}
	}
	if retention, err := metricstore.GetRetentionSummary(ctx); err == nil {
		summary.RetentionDays = retention.MaxDays
	}
	return summary
}

func buildDashboardServers(clientList []models.Client) dashboardServerSummary {
	onlineSet := make(map[string]struct{})
	for _, uuid := range agent_runtime.GetAllOnlineUUIDs() {
		onlineSet[uuid] = struct{}{}
	}
	latest := agent_runtime.GetLatestReport()
	summary := dashboardServerSummary{
		Total:        len(clientList),
		OfflineNodes: make([]dashboardOfflineNode, 0),
	}
	for _, client := range clientList {
		if _, online := onlineSet[client.UUID]; online {
			summary.Online++
			continue
		}
		node := dashboardOfflineNode{UUID: client.UUID, Name: client.Name, Region: client.Region}
		if report := latest[client.UUID]; report != nil && !report.UpdatedAt.IsZero() {
			lastSeen := report.UpdatedAt.UTC()
			node.LastSeen = &lastSeen
		}
		summary.OfflineNodes = append(summary.OfflineNodes, node)
	}
	summary.Offline = len(summary.OfflineNodes)
	sort.SliceStable(summary.OfflineNodes, func(i, j int) bool {
		left, right := summary.OfflineNodes[i], summary.OfflineNodes[j]
		if left.LastSeen == nil {
			return right.LastSeen != nil
		}
		if right.LastSeen == nil {
			return false
		}
		return left.LastSeen.Before(*right.LastSeen)
	})
	return summary
}

func loadDashboardTraffic(ctx context.Context, clientList []models.Client, now time.Time, rankingLimit int) (dashboardTrafficSummary, error) {
	today := trafficledger.BeijingDay(now)
	start := today.AddDate(0, 0, -(trafficledger.DashboardHistoryDays - 1))
	db := dbcore.GetDBInstance()
	var rows []models.TrafficDailyLedger
	if err := db.WithContext(ctx).
		Select("client", "day", "up_bytes", "down_bytes").
		Where("day >= ? AND day < ?", start.Format(time.DateOnly), today.Format(time.DateOnly)).
		Find(&rows).Error; err != nil {
		return dashboardTrafficSummary{}, fmt.Errorf("read dashboard traffic ledger: %w", err)
	}
	adjustments, err := trafficledger.DailyAdjustments(ctx, db, start, today.AddDate(0, 0, 1))
	if err != nil {
		return dashboardTrafficSummary{}, fmt.Errorf("read dashboard traffic calibration: %w", err)
	}

	todayUsage := make(map[string]trafficledger.Usage, len(clientList))
	todayHourly := make(map[string][]trafficledger.HourlyUsage, len(clientList))
	clientIDs := make([]string, 0, len(clientList))
	for _, client := range clientList {
		clientIDs = append(clientIDs, client.UUID)
	}
	todayUsage, todayHourly, err = trafficledger.MetricUsageByHourBatch(ctx, clientIDs, today.UTC(), now.UTC())
	if err != nil {
		return dashboardTrafficSummary{}, fmt.Errorf("read today's dashboard traffic: %w", err)
	}
	return summarizeDashboardTraffic(clientList, rows, todayUsage, todayHourly, adjustments, now, rankingLimit), nil
}

func summarizeDashboardTraffic(clientList []models.Client, rows []models.TrafficDailyLedger, todayUsage map[string]trafficledger.Usage, todayHourly map[string][]trafficledger.HourlyUsage, adjustments map[string]trafficledger.SignedUsage, now time.Time, rankingLimit int) dashboardTrafficSummary {
	today := trafficledger.BeijingDay(now)
	start := today.AddDate(0, 0, -(trafficledger.DashboardHistoryDays - 1))
	clientsByID := make(map[string]models.Client, len(clientList))
	for _, client := range clientList {
		clientsByID[client.UUID] = client
	}

	daysByKey := make(map[string]*dashboardTrafficDay, trafficledger.DashboardHistoryDays)
	summary := dashboardTrafficSummary{
		Daily:  make([]dashboardTrafficDay, 0, trafficledger.DashboardHistoryDays),
		Hourly: make([]dashboardTrafficHour, now.In(trafficledger.BeijingLocation).Hour()+1),
	}
	for hour := range summary.Hourly {
		summary.Hourly[hour].Hour = fmt.Sprintf("%02d:00", hour)
	}
	for day := start; !day.After(today); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		summary.Daily = append(summary.Daily, dashboardTrafficDay{Day: key})
		daysByKey[key] = &summary.Daily[len(summary.Daily)-1]
	}

	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		client, ok := clientsByID[row.Client]
		day := daysByKey[row.Day]
		if !ok || day == nil {
			continue
		}
		usage := trafficledger.ApplyAdjustment(
			trafficledger.Usage{Up: row.UpBytes, Down: row.DownBytes},
			adjustments[row.Client+"\x00"+row.Day],
		)
		day.Up += usage.Up
		day.Down += usage.Down
		if client.Price > 0 {
			day.Billable += trafficledger.BillableUsage(client.TrafficLimitType, usage.Up, usage.Down)
		}
		seen[row.Client+"\x00"+row.Day] = struct{}{}
	}

	todayKey := today.Format(time.DateOnly)
	todayDay := daysByKey[todayKey]
	todayItems := make([]dashboardTrafficRankItem, 0, len(clientList))
	for _, client := range clientList {
		adjustment := adjustments[client.UUID+"\x00"+todayKey]
		usage := trafficledger.ApplyAdjustment(todayUsage[client.UUID], adjustment)
		if todayDay != nil {
			todayDay.Up += usage.Up
			todayDay.Down += usage.Down
		}
		billable := int64(0)
		if client.Price > 0 {
			billable = trafficledger.BillableUsage(client.TrafficLimitType, usage.Up, usage.Down)
		}
		if todayDay != nil {
			todayDay.Billable += billable
		}
		summary.TodayUp += usage.Up
		summary.TodayDown += usage.Down
		summary.TodayBillable += billable
		rankingBillable := trafficledger.BillableUsage(client.TrafficLimitType, usage.Up, usage.Down)
		item := dashboardTrafficRankBase(client)
		item.Up = usage.Up
		item.Down = usage.Down
		item.Billable = rankingBillable
		todayItems = append(todayItems, item)
		if rankingBillable > 0 {
			summary.Ranking = dashboardTopTraffic(summary.Ranking, item, rankingLimit)
		}
		for _, hourly := range trafficledger.ApplyHourlyAdjustment(todayHourly[client.UUID], adjustment, now) {
			hour := hourly.Hour.In(trafficledger.BeijingLocation).Hour()
			if hour >= 0 && hour < len(summary.Hourly) {
				summary.Hourly[hour].Up += hourly.Up
				summary.Hourly[hour].Down += hourly.Down
			}
		}
	}
	for hour := 1; hour < len(summary.Hourly); hour++ {
		summary.Hourly[hour].Up += summary.Hourly[hour-1].Up
		summary.Hourly[hour].Down += summary.Hourly[hour-1].Down
	}

	expectedRows := len(clientList) * (trafficledger.DashboardHistoryDays - 1)
	summary.HistoryReady = len(seen) == expectedRows
	fillDashboardTrafficPeriod(&summary)
	storeDashboardTodayTraffic(todayKey, todayItems)
	return summary
}

func dashboardTopTraffic(top []dashboardTrafficRankItem, item dashboardTrafficRankItem, limit int) []dashboardTrafficRankItem {
	return dashboardInsertRanked(top, item, limit, func(candidate, current dashboardTrafficRankItem) bool {
		return candidate.Billable > current.Billable || (candidate.Billable == current.Billable && candidate.Name < current.Name)
	})
}
