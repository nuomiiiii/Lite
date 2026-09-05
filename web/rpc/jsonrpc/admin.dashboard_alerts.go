package jsonrpc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

type dashboardAlertLatest struct {
	Title      string     `json:"title"`
	NodeName   string     `json:"node_name,omitempty"`
	NodeUUID   string     `json:"node_uuid,omitempty"`
	TaskID     uint       `json:"task_id,omitempty"`
	TaskName   string     `json:"task_name,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	DueAt      *time.Time `json:"due_at,omitempty"`
}

type dashboardAlertAffectedItem struct {
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	NodeUUID   string     `json:"node_uuid,omitempty"`
	NodeName   string     `json:"node_name,omitempty"`
	TaskID     uint       `json:"task_id,omitempty"`
	TaskName   string     `json:"task_name,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	DueAt      *time.Time `json:"due_at,omitempty"`
}

type dashboardAlertSummary struct {
	Current        int                          `json:"current"`
	AffectedNodes  int                          `json:"affected_nodes"`
	RecoveredToday int                          `json:"recovered_today"`
	LatestAlert    *dashboardAlertLatest        `json:"latest_alert,omitempty"`
	Error          string                       `json:"error,omitempty"`
	Items          []dashboardAlertAffectedItem `json:"-"`
}

type dashboardAlertSummaries struct {
	Resource    dashboardAlertSummary `json:"resource"`
	Offline     dashboardAlertSummary `json:"offline"`
	LatencyLoss dashboardAlertSummary `json:"latency_loss"`
	Traffic     dashboardAlertSummary `json:"traffic"`
	ReturnRoute dashboardAlertSummary `json:"return_route"`
	Billing     dashboardAlertSummary `json:"billing"`
}

type dashboardAlertItemsResponse struct {
	Kind        string                       `json:"kind"`
	Items       []dashboardAlertAffectedItem `json:"items"`
	GeneratedAt time.Time                    `json:"generated_at"`
}

var dashboardAlertKinds = map[string]struct{}{
	"offline": {}, "resource": {}, "latency_loss": {}, "traffic": {}, "return_route": {}, "billing": {},
}

func adminGetDashboardAlertItems(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	rawKind, _ := rpc.GetParamAs[string](req, "kind")
	kind := strings.ToLower(strings.TrimSpace(rawKind))
	if _, ok := dashboardAlertKinds[kind]; !ok {
		return nil, rpc.MakeError(rpc.InvalidParams, "unsupported dashboard alert kind", nil)
	}
	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	now := time.Now().UTC()
	clientByID := make(map[string]models.Client, len(clientList))
	for _, client := range clientList {
		clientByID[client.UUID] = client
	}
	reports := agent_runtime.GetLatestReport()
	var summary dashboardAlertSummary
	switch kind {
	case "offline":
		summary = buildDashboardOfflineAlerts(clientByID, now)
	case "resource":
		summary = buildDashboardResourceAlerts(clientByID, reports)
	case "latency_loss":
		summary = buildDashboardLatencyAlerts(now)
	case "traffic":
		summary = buildDashboardTrafficAlerts(clientList, reports, now)
	case "return_route":
		summary = buildDashboardReturnRouteAlerts(now)
	case "billing":
		summary = buildDashboardBillingAlerts(clientList, now)
	}
	if summary.Error != "" {
		return nil, rpc.MakeError(rpc.InternalError, summary.Error, nil)
	}
	if summary.Items == nil {
		summary.Items = []dashboardAlertAffectedItem{}
	}
	return dashboardAlertItemsResponse{Kind: kind, Items: summary.Items, GeneratedAt: now}, nil
}

func buildDashboardAlerts(clientList []models.Client, now time.Time) dashboardAlertSummaries {
	clientByID := make(map[string]models.Client, len(clientList))
	for _, client := range clientList {
		clientByID[client.UUID] = client
	}
	reports := agent_runtime.GetLatestReport()
	return dashboardAlertSummaries{
		Resource:    buildDashboardResourceAlerts(clientByID, reports),
		Offline:     buildDashboardOfflineAlerts(clientByID, now),
		LatencyLoss: buildDashboardLatencyAlerts(now),
		Traffic:     buildDashboardTrafficAlerts(clientList, reports, now),
		ReturnRoute: buildDashboardReturnRouteAlerts(now),
		Billing:     buildDashboardBillingAlerts(clientList, now),
	}
}

func buildDashboardResourceAlerts(clientByID map[string]models.Client, reports map[string]*v2.Report) dashboardAlertSummary {
	var rules []models.LoadNotification
	if err := dbcore.GetDBInstance().Find(&rules).Error; err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	result := dashboardAlertSummary{}
	affected := make(map[string]struct{})
	for _, rule := range rules {
		for _, clientID := range rule.Clients {
			report := reports[clientID]
			client, exists := clientByID[clientID]
			if report == nil || !exists {
				continue
			}
			value, ok := dashboardResourceValue(report, client, rule.Metric)
			if !ok || value < float64(rule.Threshold) {
				continue
			}
			title := strings.TrimSpace(rule.Name)
			if title == "" {
				title = strings.ToUpper(strings.TrimSpace(rule.Metric))
			}
			if _, seen := affected[clientID]; !seen {
				affected[clientID] = struct{}{}
				result.Items = append(result.Items, dashboardAlertAffectedItem{
					Kind: "resource", Title: title, NodeUUID: clientID, NodeName: client.Name,
				})
			}
			setDashboardLatest(&result, title, client.Name, clientID, 0, "", report.UpdatedAt)
		}
	}
	result.Current = len(result.Items)
	result.AffectedNodes = len(affected)
	return result
}

func dashboardResourceValue(report *v2.Report, client models.Client, metricName string) (float64, bool) {
	percentage := func(used, total int64) (float64, bool) {
		if total <= 0 {
			return 0, false
		}
		return float64(used) / float64(total) * 100, true
	}
	switch strings.ToLower(strings.TrimSpace(metricName)) {
	case "cpu":
		return report.CPU.Usage, true
	case "gpu":
		if report.GPU == nil {
			return 0, false
		}
		return report.GPU.AverageUsage, true
	case "ram":
		total := report.Ram.Total
		if total <= 0 {
			total = client.MemTotal
		}
		return percentage(report.Ram.Used, total)
	case "swap":
		total := report.Swap.Total
		if total <= 0 {
			total = client.SwapTotal
		}
		return percentage(report.Swap.Used, total)
	case "disk":
		total := report.Disk.Total
		if total <= 0 {
			total = client.DiskTotal
		}
		return percentage(report.Disk.Used, total)
	case "load":
		return report.Load.Load1, true
	case "net_in", "netin":
		return float64(report.Network.Down) * 8 / 1_000_000, true
	case "net_out", "netout":
		return float64(report.Network.Up) * 8 / 1_000_000, true
	default:
		return 0, false
	}
}

func buildDashboardOfflineAlerts(clientByID map[string]models.Client, now time.Time) dashboardAlertSummary {
	var notifications []models.OfflineNotification
	if err := dbcore.GetDBInstance().Where("enable = ?", true).Find(&notifications).Error; err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	online := make(map[string]struct{})
	for _, clientID := range agent_runtime.GetAllOnlineUUIDs() {
		online[clientID] = struct{}{}
	}
	result := dashboardAlertSummary{}
	for _, notification := range notifications {
		client, exists := clientByID[notification.Client]
		if !exists {
			continue
		}
		if _, isOnline := online[notification.Client]; isOnline {
			if notification.LastNotified != nil && sameDashboardDay(*notification.LastNotified, now) {
				result.RecoveredToday++
			}
			continue
		}
		result.Current++
		result.AffectedNodes++
		result.Items = append(result.Items, dashboardAlertAffectedItem{
			Kind: "offline", Title: "offline", NodeUUID: notification.Client, NodeName: client.Name,
		})
		if notification.LastNotified != nil {
			setDashboardLatest(&result, "offline", client.Name, notification.Client, 0, "", *notification.LastNotified)
		}
	}
	return result
}

func buildDashboardLatencyAlerts(now time.Time) dashboardAlertSummary {
	var notifications []models.PingLossNotification
	if err := dbcore.GetDBInstance().Preload("ClientInfo").Preload("Task").Where("enable = ?", true).Find(&notifications).Error; err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	result := dashboardAlertSummary{}
	affected := make(map[string]struct{})
	for _, notification := range notifications {
		if !notification.AlertActive {
			if notification.LastNotified != nil && sameDashboardDay(*notification.LastNotified, now) {
				result.RecoveredToday++
			}
			continue
		}
		result.Current++
		affected[notification.Client] = struct{}{}
		result.Items = append(result.Items, dashboardAlertAffectedItem{
			Kind: "latency_loss", Title: "ping loss", NodeUUID: notification.Client,
			NodeName: notification.ClientInfo.Name, TaskID: notification.TaskId, TaskName: notification.Task.Name,
		})
		if notification.LastNotified != nil {
			setDashboardLatest(&result, "ping loss", notification.ClientInfo.Name, notification.Client,
				notification.TaskId, notification.Task.Name, *notification.LastNotified)
		}
	}
	result.AffectedNodes = len(affected)
	return result
}

func buildDashboardTrafficAlerts(clientList []models.Client, reports map[string]*v2.Report, now time.Time) dashboardAlertSummary {
	enabled, err := config.GetAs[bool](config.NotificationEnabledKey, true)
	if err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	threshold, err := config.GetAs[float64](config.TrafficLimitPercentageKey, 80.0)
	if err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	if !enabled || threshold <= 0 {
		return dashboardAlertSummary{}
	}
	calibrated, calibrationErr := trafficledger.CurrentCalibratedCycleUsages(
		context.Background(), dbcore.GetDBInstance(), now,
	)
	result := dashboardAlertSummary{}
	if calibrationErr != nil {
		result.Error = calibrationErr.Error()
	}
	for _, client := range clientList {
		report := reports[client.UUID]
		if report == nil {
			continue
		}
		limit, limitType := clients.EffectiveTrafficLimit(client, now)
		if limit <= 0 {
			continue
		}
		usage := trafficledger.Usage{Up: report.Network.TotalUp, Down: report.Network.TotalDown}
		if value, ok := calibrated[client.UUID]; ok {
			usage = value
		}
		used := trafficledger.BillableUsage(limitType, usage.Up, usage.Down)
		percent := float64(used) / float64(limit) * 100
		if percent < threshold {
			continue
		}
		result.Current++
		result.AffectedNodes++
		title := fmt.Sprintf("%.0f%%", percent)
		result.Items = append(result.Items, dashboardAlertAffectedItem{
			Kind: "traffic", Title: title, NodeUUID: client.UUID, NodeName: client.Name,
		})
		setDashboardLatest(&result, title, client.Name, client.UUID, 0, "", report.UpdatedAt)
	}
	return result
}

func buildDashboardReturnRouteAlerts(now time.Time) dashboardAlertSummary {
	summary, err := tasks.GetReturnRouteSummary()
	if err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	result := dashboardAlertSummary{Current: int(summary.Switched)}
	db := dbcore.GetDBInstance()
	if err := db.Table("return_route_statuses").
		Select("COUNT(DISTINCT return_route_tasks.client)").
		Joins("JOIN return_route_tasks ON return_route_tasks.id = return_route_statuses.task_id").
		Where("return_route_tasks.enabled = ? AND return_route_statuses.state = ?", true, "switched").
		Scan(&result.AffectedNodes).Error; err != nil {
		result.Error = err.Error()
	}
	var switchedTasks []models.ReturnRouteTask
	switchedTaskIDs := db.Model(&models.ReturnRouteStatus{}).Select("task_id").Where("state = ?", "switched")
	if err := db.Preload("ClientInfo").Where("enabled = ? AND id IN (?)", true, switchedTaskIDs).Order("id ASC").Find(&switchedTasks).Error; err != nil {
		if result.Error == "" {
			result.Error = err.Error()
		}
	} else {
		for _, task := range switchedTasks {
			result.Items = append(result.Items, dashboardAlertAffectedItem{
				Kind: "return_route", Title: task.Name, NodeUUID: task.Client,
				NodeName: task.ClientInfo.Name, TaskID: task.Id, TaskName: task.Name,
			})
		}
		result.Current = len(result.Items)
	}
	dayStart := trafficledger.BeijingDay(now).UTC()
	dayEnd := trafficledger.BeijingDay(now).AddDate(0, 0, 1).UTC()
	recoveries, err := tasks.QueryReturnRouteEvents(tasks.ReturnRouteEventQuery{
		Page: 1, PageSize: 1, Kind: "recovery", Start: &dayStart, End: &dayEnd,
	})
	if err == nil {
		result.RecoveredToday = int(recoveries.Total)
	} else if result.Error == "" {
		result.Error = err.Error()
	}
	latest, err := tasks.QueryReturnRouteEvents(tasks.ReturnRouteEventQuery{Page: 1, PageSize: 1, Kind: "switch"})
	if err == nil && len(latest.Events) > 0 {
		event := latest.Events[0]
		occurredAt := event.OccurredAt.UTC()
		result.LatestAlert = &dashboardAlertLatest{
			Title:      strings.TrimSpace(event.FromLine + " -> " + event.ToLine),
			NodeName:   event.NodeName,
			NodeUUID:   event.Client,
			TaskID:     event.TaskId,
			TaskName:   event.TaskName,
			OccurredAt: &occurredAt,
		}
	} else if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	return result
}

func buildDashboardBillingAlerts(clientList []models.Client, now time.Time) dashboardAlertSummary {
	enabled, err := config.GetAs[bool](config.ExpireNotificationEnabledKey, true)
	if err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	leadDays, err := config.GetAs[int](config.ExpireNotificationLeadDaysKey, 7)
	if err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	if !enabled || leadDays < 0 {
		return dashboardAlertSummary{}
	}
	deadline := now.AddDate(0, 0, leadDays)
	result := dashboardAlertSummary{}
	var nearest *models.Client
	for index := range clientList {
		client := &clientList[index]
		if client.ExpiredAt == nil || client.ExpiredAt.After(deadline) {
			continue
		}
		result.Current++
		result.AffectedNodes++
		dueAt := client.ExpiredAt.UTC()
		title := dashboardBillingAlertTitle(dueAt, now)
		result.Items = append(result.Items, dashboardAlertAffectedItem{
			Kind: "billing", Title: title, NodeUUID: client.UUID, NodeName: client.Name, DueAt: &dueAt,
		})
		if nearest == nil || client.ExpiredAt.Before(*nearest.ExpiredAt) {
			nearest = client
		}
	}
	sort.SliceStable(result.Items, func(left, right int) bool {
		return result.Items[left].DueAt.Before(*result.Items[right].DueAt)
	})
	if nearest != nil {
		dueAt := nearest.ExpiredAt.UTC()
		result.LatestAlert = &dashboardAlertLatest{
			Title:    dashboardBillingAlertTitle(dueAt, now),
			NodeName: nearest.Name,
			NodeUUID: nearest.UUID,
			DueAt:    &dueAt,
		}
	}
	return result
}

func dashboardBillingAlertTitle(expiry, now time.Time) string {
	hours := expiry.Sub(now).Hours()
	if hours < 0 {
		return fmt.Sprintf("expired %d days", int(math.Ceil(-hours/24)))
	}
	return fmt.Sprintf("%d days left", int(math.Ceil(hours/24)))
}

func setDashboardLatest(summary *dashboardAlertSummary, title, nodeName, nodeUUID string, taskID uint, taskName string, occurredAt time.Time) {
	if occurredAt.IsZero() {
		return
	}
	when := occurredAt.UTC()
	if summary.LatestAlert != nil && summary.LatestAlert.OccurredAt != nil && !when.After(*summary.LatestAlert.OccurredAt) {
		return
	}
	summary.LatestAlert = &dashboardAlertLatest{
		Title: title, NodeName: nodeName, NodeUUID: nodeUUID, TaskID: taskID, TaskName: taskName, OccurredAt: &when,
	}
}

func sameDashboardDay(left, right time.Time) bool {
	return trafficledger.BeijingDay(left).Equal(trafficledger.BeijingDay(right))
}
