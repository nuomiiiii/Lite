package jsonrpc

import (
	"context"
	"fmt"
	"sort"
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

type dashboardTrafficSummary struct {
	TodayUp       int64                  `json:"today_up"`
	TodayDown     int64                  `json:"today_down"`
	TodayBillable int64                  `json:"today_billable"`
	Hourly        []dashboardTrafficHour `json:"hourly"`
	Daily         []dashboardTrafficDay  `json:"daily"`
	HistoryReady  bool                   `json:"history_ready"`
	Error         string                 `json:"error,omitempty"`
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

type dashboardResponse struct {
	Servers     dashboardServerSummary      `json:"servers"`
	Database    databaseStatusResponse      `json:"database"`
	Storage     dashboardStorageSummary     `json:"storage"`
	ReturnRoute dashboardReturnRouteSummary `json:"return_route"`
	Alerts      dashboardAlertSummaries     `json:"alerts"`
	GeneratedAt time.Time                   `json:"generated_at"`
}

type dashboardChartsResponse struct {
	Traffic     dashboardTrafficSummary `json:"traffic"`
	Latency     dashboardLatencySummary `json:"latency"`
	GeneratedAt time.Time               `json:"generated_at"`
}

var dashboardCache struct {
	sync.Mutex
	value dashboardResponse
	at    time.Time
	valid bool
}

var dashboardChartsCache struct {
	sync.Mutex
	value dashboardChartsResponse
	at    time.Time
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

func adminGetDashboard(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	now := time.Now().UTC()
	dashboardCache.Lock()
	defer dashboardCache.Unlock()
	if dashboardCache.valid && now.Sub(dashboardCache.at) < dashboardSummaryCacheTTL {
		return dashboardCache.value, nil
	}

	value, err := buildDashboard(ctx, now)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	dashboardCache.value = value
	dashboardCache.at = now
	dashboardCache.valid = true
	return value, nil
}

func adminGetDashboardCharts(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	now := time.Now().UTC()
	dashboardChartsCache.Lock()
	defer dashboardChartsCache.Unlock()
	if dashboardChartsCache.valid && now.Sub(dashboardChartsCache.at) < dashboardChartsCacheTTL {
		return dashboardChartsCache.value, nil
	}

	value := buildDashboardCharts(ctx, now)
	dashboardChartsCache.value = value
	dashboardChartsCache.at = now
	dashboardChartsCache.valid = true
	return value, nil
}

func buildDashboard(ctx context.Context, now time.Time) (dashboardResponse, error) {
	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return dashboardResponse{}, fmt.Errorf("list dashboard clients: %w", err)
	}

	main := mainDatabaseStatus()
	monitoring := monitoringDatabaseStatus(ctx)
	legacySize := int64(0)
	if main.Size != nil {
		legacySize = *main.Size
	}

	return dashboardResponse{
		Servers: buildDashboardServers(clientList),
		Database: databaseStatusResponse{
			Type:       main.Driver,
			Size:       legacySize,
			Main:       main,
			Monitoring: monitoring,
			LocalTotal: localDatabaseTotal(main, monitoring),
		},
		Storage:     buildDashboardStorage(ctx, main, monitoring),
		ReturnRoute: buildDashboardReturnRoute(),
		Alerts:      buildDashboardAlerts(clientList, now),
		GeneratedAt: now,
	}, nil
}

func buildDashboardCharts(ctx context.Context, now time.Time) dashboardChartsResponse {
	result := dashboardChartsResponse{GeneratedAt: now}
	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		message := fmt.Sprintf("list dashboard clients: %v", err)
		result.Traffic.Error = message
		result.Latency.Error = message
		return result
	}
	if result.Traffic, err = loadDashboardTraffic(ctx, clientList, now); err != nil {
		result.Traffic = dashboardTrafficSummary{Error: err.Error()}
	}
	if result.Latency, err = loadDashboardLatency(ctx, clientList, now); err != nil {
		result.Latency.Error = err.Error()
	}
	return result
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

func loadDashboardTraffic(ctx context.Context, clientList []models.Client, now time.Time) (dashboardTrafficSummary, error) {
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
	return summarizeDashboardTraffic(clientList, rows, todayUsage, todayHourly, adjustments, now), nil
}

func summarizeDashboardTraffic(clientList []models.Client, rows []models.TrafficDailyLedger, todayUsage map[string]trafficledger.Usage, todayHourly map[string][]trafficledger.HourlyUsage, adjustments map[string]trafficledger.SignedUsage, now time.Time) dashboardTrafficSummary {
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
