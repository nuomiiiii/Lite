package jsonrpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/database/trafficledger"
	"github.com/komari-monitor/komari/pkg/config"
	v1 "github.com/komari-monitor/komari/protocol/v1"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
)

type dashboardAlertLatest struct {
	Title      string     `json:"title"`
	NodeName   string     `json:"node_name,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
}

type dashboardAlertSummary struct {
	Current        int                   `json:"current"`
	AffectedNodes  int                   `json:"affected_nodes"`
	RecoveredToday int                   `json:"recovered_today"`
	LatestAlert    *dashboardAlertLatest `json:"latest_alert,omitempty"`
	Error          string                `json:"error,omitempty"`
}

type dashboardAlertSummaries struct {
	Resource    dashboardAlertSummary `json:"resource"`
	Offline     dashboardAlertSummary `json:"offline"`
	LatencyLoss dashboardAlertSummary `json:"latency_loss"`
	Traffic     dashboardAlertSummary `json:"traffic"`
	ReturnRoute dashboardAlertSummary `json:"return_route"`
	Billing     dashboardAlertSummary `json:"billing"`
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

func buildDashboardResourceAlerts(clientByID map[string]models.Client, reports map[string]*v1.Report) dashboardAlertSummary {
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
			result.Current++
			affected[clientID] = struct{}{}
			title := strings.TrimSpace(rule.Name)
			if title == "" {
				title = strings.ToUpper(strings.TrimSpace(rule.Metric))
			}
			setDashboardLatest(&result, title, client.Name, report.UpdatedAt)
		}
	}
	result.AffectedNodes = len(affected)
	return result
}

func dashboardResourceValue(report *v1.Report, client models.Client, metricName string) (float64, bool) {
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
		if notification.LastNotified != nil {
			setDashboardLatest(&result, "offline", client.Name, *notification.LastNotified)
		}
	}
	return result
}

func buildDashboardLatencyAlerts(now time.Time) dashboardAlertSummary {
	var notifications []models.PingLossNotification
	if err := dbcore.GetDBInstance().Preload("ClientInfo").Where("enable = ?", true).Find(&notifications).Error; err != nil {
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
		if notification.LastNotified != nil {
			setDashboardLatest(&result, "ping loss", notification.ClientInfo.Name, *notification.LastNotified)
		}
	}
	result.AffectedNodes = len(affected)
	return result
}

func buildDashboardTrafficAlerts(clientList []models.Client, reports map[string]*v1.Report, now time.Time) dashboardAlertSummary {
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
		setDashboardLatest(&result, fmt.Sprintf("%.0f%%", percent), client.Name, report.UpdatedAt)
	}
	return result
}

func buildDashboardReturnRouteAlerts(now time.Time) dashboardAlertSummary {
	summary, err := tasks.GetReturnRouteSummary()
	if err != nil {
		return dashboardAlertSummary{Error: err.Error()}
	}
	result := dashboardAlertSummary{Current: int(summary.Switched + summary.Abnormal)}
	db := dbcore.GetDBInstance()
	if err := db.Table("return_route_statuses").
		Select("COUNT(DISTINCT return_route_tasks.client)").
		Joins("JOIN return_route_tasks ON return_route_tasks.id = return_route_statuses.task_id").
		Where("return_route_tasks.enabled = ? AND (return_route_statuses.state IN ? OR (return_route_statuses.last_error IS NOT NULL AND return_route_statuses.last_error <> ?))", true, []string{"switched", "unknown"}, "").
		Scan(&result.AffectedNodes).Error; err != nil {
		result.Error = err.Error()
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
		if client.ExpiredAt == nil || client.ExpiredAt.Before(now) || client.ExpiredAt.After(deadline) {
			continue
		}
		result.Current++
		result.AffectedNodes++
		if nearest == nil || client.ExpiredAt.Before(*nearest.ExpiredAt) {
			nearest = client
		}
	}
	if nearest != nil {
		days := int(nearest.ExpiredAt.Sub(now).Hours() / 24)
		result.LatestAlert = &dashboardAlertLatest{
			Title:    fmt.Sprintf("%d days left", days),
			NodeName: nearest.Name,
		}
	}
	return result
}

func setDashboardLatest(summary *dashboardAlertSummary, title, nodeName string, occurredAt time.Time) {
	if occurredAt.IsZero() {
		return
	}
	when := occurredAt.UTC()
	if summary.LatestAlert != nil && summary.LatestAlert.OccurredAt != nil && !when.After(*summary.LatestAlert.OccurredAt) {
		return
	}
	summary.LatestAlert = &dashboardAlertLatest{Title: title, NodeName: nodeName, OccurredAt: &when}
}

func sameDashboardDay(left, right time.Time) bool {
	return trafficledger.BeijingDay(left).Equal(trafficledger.BeijingDay(right))
}
