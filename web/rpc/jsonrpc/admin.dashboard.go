package jsonrpc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/database/trafficledger"
	"github.com/komari-monitor/komari/pkg/rpc"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
)

const (
	dashboardSummaryCacheTTL = 5 * time.Second
	dashboardChartsCacheTTL  = 30 * time.Second
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
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Up       int64  `json:"up"`
	Down     int64  `json:"down"`
	Billable int64  `json:"billable"`
}

type dashboardTrafficSummary struct {
	TodayUp       int64                      `json:"today_up"`
	TodayDown     int64                      `json:"today_down"`
	TodayBillable int64                      `json:"today_billable"`
	Hourly        []dashboardTrafficHour     `json:"hourly"`
	Daily         []dashboardTrafficDay      `json:"daily"`
	Ranking       []dashboardTrafficRankItem `json:"ranking"`
	HistoryReady  bool                       `json:"history_ready"`
	Error         string                     `json:"error,omitempty"`
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
	UUID   string  `json:"uuid"`
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Disk   float64 `json:"disk"`
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
	dashboardChartAll = dashboardChartTraffic | dashboardChartLatency | dashboardChartLatencyJitter
)

type dashboardChartsResponse struct {
	Traffic     dashboardTrafficSummary `json:"traffic"`
	Latency     dashboardLatencySummary `json:"latency"`
	GeneratedAt time.Time               `json:"generated_at"`
}

var dashboardCache struct {
	sync.Mutex
	value dashboardResponse
	at    time.Time
	key   string
	valid bool
}

var dashboardChartsCache struct {
	sync.Mutex
	value dashboardChartsResponse
	at    time.Time
	key   string
	valid bool
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
}

func adminGetDashboard(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	now := time.Now().UTC()
	sections, rankingLimit := parseDashboardSummaryRequest(req)
	cacheKey := fmt.Sprintf("%d:%d", sections, rankingLimit)
	dashboardCache.Lock()
	defer dashboardCache.Unlock()
	if dashboardCache.valid && dashboardCache.key == cacheKey && now.Sub(dashboardCache.at) < dashboardSummaryCacheTTL {
		return dashboardCache.value, nil
	}

	value, err := buildDashboard(ctx, now, sections, rankingLimit)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	dashboardCache.value = value
	dashboardCache.at = now
	dashboardCache.key = cacheKey
	dashboardCache.valid = true
	return value, nil
}

func adminGetDashboardCharts(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	now := time.Now().UTC()
	sections, rankingLimit := parseDashboardChartRequest(req)
	cacheKey := fmt.Sprintf("%d:%d", sections, rankingLimit)
	dashboardChartsCache.Lock()
	defer dashboardChartsCache.Unlock()
	if dashboardChartsCache.valid && dashboardChartsCache.key == cacheKey && now.Sub(dashboardChartsCache.at) < dashboardChartsCacheTTL {
		return dashboardChartsCache.value, nil
	}

	value := buildDashboardCharts(ctx, now, sections, rankingLimit)
	dashboardChartsCache.value = value
	dashboardChartsCache.at = now
	dashboardChartsCache.key = cacheKey
	dashboardChartsCache.valid = true
	return value, nil
}

func buildDashboard(ctx context.Context, now time.Time, sections dashboardSummarySections, rankingLimit int) (dashboardResponse, error) {
	result := dashboardResponse{GeneratedAt: now}
	needsClients := sections&(dashboardSectionServers|dashboardSectionResources|dashboardSectionAlerts) != 0
	var clientList []models.Client
	var err error
	if needsClients {
		clientList, err = clients.GetAllClientBasicInfo()
		if err != nil {
			return dashboardResponse{}, fmt.Errorf("list dashboard clients: %w", err)
		}
	}
	if sections&dashboardSectionServers != 0 {
		result.Servers = buildDashboardServers(clientList)
	}
	if sections&dashboardSectionResources != 0 {
		result.Resources = buildDashboardResources(clientList, rankingLimit)
	}
	if sections&dashboardSectionStorage != 0 {
		main := mainDatabaseStatus()
		monitoring := monitoringDatabaseStatus(ctx)
		legacySize := int64(0)
		if main.Size != nil {
			legacySize = *main.Size
		}
		result.Database = databaseStatusResponse{
			Type:       main.Driver,
			Size:       legacySize,
			Main:       main,
			Monitoring: monitoring,
			LocalTotal: localDatabaseTotal(main, monitoring),
		}
		result.Storage = buildDashboardStorage(ctx, main, monitoring)
	}
	if sections&dashboardSectionReturnRoute != 0 {
		result.ReturnRoute = buildDashboardReturnRoute()
	}
	if sections&dashboardSectionAlerts != 0 {
		result.Alerts = buildDashboardAlerts(clientList, now)
	}
	return result, nil
}

func buildDashboardCharts(ctx context.Context, now time.Time, sections dashboardChartSections, rankingLimit int) dashboardChartsResponse {
	result := dashboardChartsResponse{GeneratedAt: now}
	if sections == 0 {
		return result
	}
	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		message := fmt.Sprintf("list dashboard clients: %v", err)
		if sections&dashboardChartTraffic != 0 {
			result.Traffic.Error = message
		}
		if sections&dashboardChartLatency != 0 {
			result.Latency.Error = message
		}
		if sections&dashboardChartLatencyJitter != 0 {
			result.Latency.JitterError = message
		}
		return result
	}
	if sections&dashboardChartTraffic != 0 {
		if result.Traffic, err = loadDashboardTraffic(ctx, clientList, now, rankingLimit); err != nil {
			result.Traffic = dashboardTrafficSummary{Error: err.Error()}
		}
	}
	if sections&dashboardChartLatency != 0 {
		if result.Latency, err = loadDashboardLatency(ctx, clientList, now, rankingLimit); err != nil {
			result.Latency.Error = err.Error()
		}
	}
	if sections&dashboardChartLatencyJitter != 0 {
		if result.Latency.JitterRanking, err = loadDashboardLatencyJitter(ctx, clientList, now, rankingLimit); err != nil {
			result.Latency.JitterError = err.Error()
		}
	}
	return result
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
		}
	}
	return sections, parseDashboardRankingLimit(req)
}

func parseDashboardRankingLimit(req *rpc.JsonRpcRequest) int {
	if rawLimit, ok := rpc.GetParamAs[string](req, "limit"); ok {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && dashboardRankingLimitAllowed(parsed) {
			return parsed
		}
	}
	return 5
}

func buildDashboardResources(clientList []models.Client, limit int) dashboardResourceSummary {
	if !dashboardRankingLimitAllowed(limit) {
		limit = 5
	}
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
	top := make([]dashboardResourceRankItem, 0, limit)
	for _, item := range items {
		insertAt := len(top)
		for index, current := range top {
			if value(item) > value(current) || (value(item) == value(current) && item.Name < current.Name) {
				insertAt = index
				break
			}
		}
		if insertAt >= limit {
			continue
		}
		if len(top) < limit {
			top = append(top, dashboardResourceRankItem{})
		}
		copy(top[insertAt+1:], top[insertAt:len(top)-1])
		top[insertAt] = item
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
	for _, client := range clientList {
		usage, hourly, err := trafficledger.MetricUsageByHour(ctx, client.UUID, today.UTC(), now.UTC())
		if err != nil {
			return dashboardTrafficSummary{}, fmt.Errorf("read today's traffic for client %s: %w", client.UUID, err)
		}
		todayUsage[client.UUID] = usage
		todayHourly[client.UUID] = hourly
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
	for _, client := range clientList {
		adjustment := adjustments[client.UUID+"\x00"+todayKey]
		usage := trafficledger.ApplyAdjustment(todayUsage[client.UUID], adjustment)
		todayDay.Up += usage.Up
		todayDay.Down += usage.Down
		billable := int64(0)
		if client.Price > 0 {
			billable = trafficledger.BillableUsage(client.TrafficLimitType, usage.Up, usage.Down)
		}
		todayDay.Billable += billable
		summary.TodayUp += usage.Up
		summary.TodayDown += usage.Down
		summary.TodayBillable += billable
		name := strings.TrimSpace(client.Name)
		if name == "" {
			name = client.UUID
		}
		rankingBillable := trafficledger.BillableUsage(client.TrafficLimitType, usage.Up, usage.Down)
		if rankingBillable > 0 {
			summary.Ranking = dashboardTopTraffic(summary.Ranking, dashboardTrafficRankItem{
				UUID: client.UUID, Name: name, Up: usage.Up, Down: usage.Down, Billable: rankingBillable,
			}, rankingLimit)
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
	return summary
}

func dashboardTopTraffic(top []dashboardTrafficRankItem, item dashboardTrafficRankItem, limit int) []dashboardTrafficRankItem {
	if !dashboardRankingLimitAllowed(limit) {
		limit = 5
	}
	insertAt := len(top)
	for index, current := range top {
		if item.Billable > current.Billable || (item.Billable == current.Billable && item.Name < current.Name) {
			insertAt = index
			break
		}
	}
	if insertAt >= limit {
		return top
	}
	if len(top) < limit {
		top = append(top, dashboardTrafficRankItem{})
	}
	copy(top[insertAt+1:], top[insertAt:len(top)-1])
	top[insertAt] = item
	return top
}
