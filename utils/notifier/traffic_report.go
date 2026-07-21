package notifier

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/pkg/corn"
	logger "github.com/komari-monitor/komari/utils/log"
	"github.com/komari-monitor/komari/utils/messageSender"
)

var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

type TrafficReportSendResult struct {
	Sent        bool `json:"sent"`
	ClientCount int  `json:"client_count"`
}

// InitTrafficReportSchedule 注册三个按北京时间执行的定时任务：日报、周报、月报。
func InitTrafficReportSchedule() {
	if err := ReloadTrafficReportSchedule(); err != nil {
		logger.ErrorArgs("notifier", "Failed to register traffic report schedules:", err)
	}
}

// ReloadTrafficReportSchedule applies the configured HH:mm time without restarting Komari.
func ReloadTrafficReportSchedule() error {
	reportTime, err := config.GetAs[string](config.TrafficReportTimeKey, config.DefaultTrafficReportTime)
	if err != nil {
		return fmt.Errorf("load traffic report time: %w", err)
	}
	reportTime, err = config.NormalizeTrafficReportTime(reportTime)
	if err != nil {
		return err
	}
	parsed, _ := time.Parse("15:04", reportTime)
	minute, hour := parsed.Minute(), parsed.Hour()

	jobs := []struct {
		name string
		spec string
		run  func()
	}{
		{"traffic-report-daily", fmt.Sprintf("0 %d %d * * *", minute, hour), func() { runScheduledTrafficReport(true, false, false) }},
		{"traffic-report-weekly", fmt.Sprintf("0 %d %d * * 1", minute, hour), func() { runScheduledTrafficReport(false, true, false) }},
		{"traffic-report-monthly", fmt.Sprintf("0 %d %d 1 * *", minute, hour), func() { runScheduledTrafficReport(false, false, true) }},
	}
	for _, scheduledJob := range jobs {
		if err := corn.AddFuncInLocation(scheduledJob.name, scheduledJob.spec, beijingLocation, scheduledJob.run); err != nil {
			return err
		}
	}
	logger.Infof("notifier", "Traffic report schedules registered for %s Asia/Shanghai", reportTime)
	return nil
}

func runScheduledTrafficReport(daily, weekly, monthly bool) {
	if _, err := sendTrafficReport(daily, weekly, monthly, false); err != nil {
		logger.Errorf("notifier", "Failed to send scheduled traffic report: %v", err)
	}
}

// SendDailyTrafficReportNow sends traffic from 00:00 Beijing time through the click time.
func SendDailyTrafficReportNow() (TrafficReportSendResult, error) {
	return sendTrafficReport(true, false, false, true)
}

// sendTrafficReport 汇聚所有启用了指定报告类型的服务器流量，合并成一条通知发送。
func sendTrafficReport(daily, weekly, monthly, currentDaily bool) (TrafficReportSendResult, error) {
	result := TrafficReportSendResult{}
	// 检查全局通知开关
	enabled, err := config.GetAs[bool](config.NotificationEnabledKey, false)
	if err != nil {
		return result, fmt.Errorf("load notification setting: %w", err)
	}
	if !enabled {
		return result, fmt.Errorf("notifications are disabled")
	}

	db := dbcore.GetDBInstance()
	now := time.Now().UTC()

	var eventType, label, suffix string

	switch {
	case daily:
		eventType = messageevent.DReport
		label = "daily"
		if currentDaily {
			suffix = "今日流量"
		} else {
			suffix = "昨日流量"
		}
	case weekly:
		eventType = messageevent.WReport
		label = "weekly"
		suffix = "上周流量"
	case monthly:
		eventType = messageevent.MReport
		label = "monthly"
		suffix = "上个月流量"
	default:
		return result, fmt.Errorf("traffic report cadence is required")
	}
	start, end := previousTrafficReportRange(now, label)
	if currentDaily {
		start, end = currentDailyTrafficReportRange(now)
	}

	// 查询所有启用该类型报告的服务器配置
	var notifications []models.TrafficReportNotification
	query := db.Model(&models.TrafficReportNotification{}).Where("enable = ?", true)
	if daily {
		query = query.Where("daily = ?", true)
	} else if weekly {
		query = query.Where("weekly = ?", true)
	} else if monthly {
		query = query.Where("monthly = ?", true)
	}
	if err := query.Find(&notifications).Error; err != nil {
		return result, fmt.Errorf("query %s traffic report notifications: %w", label, err)
	}
	if len(notifications) == 0 {
		return result, nil
	}

	// 获取客户端信息
	clientUUIDs := make([]string, 0, len(notifications))
	for _, n := range notifications {
		clientUUIDs = append(clientUUIDs, n.Client)
	}
	var clientList []models.Client
	if err := db.Where("uuid IN ?", clientUUIDs).Find(&clientList).Error; err != nil {
		return result, fmt.Errorf("query clients for %s traffic report: %w", label, err)
	}
	clientMap := make(map[string]models.Client, len(clientList))
	for _, c := range clientList {
		clientMap[c.UUID] = c
	}

	// 为每个服务器统计流量并拼接消息
	var lines []string
	eventClients := make([]models.Client, 0, len(notifications))
	var lastClientError error
	for _, n := range notifications {
		c, ok := clientMap[n.Client]
		if !ok {
			continue
		}

		usage, err := getClientTrafficInRange(n.Client, start, end)
		if err != nil {
			logger.Errorf("notifier", "Failed to compute traffic for client %s (%s): %v", n.Client, label, err)
			lastClientError = err
			continue
		}

		lines = append(lines, formatTrafficReportLine(c, suffix, usage, n.IncludeTraffic, n.IncludeBilling))
		eventClients = append(eventClients, c)
	}

	if len(lines) == 0 {
		if lastClientError != nil {
			return result, fmt.Errorf("compute traffic report: %w", lastClientError)
		}
		return result, nil
	}

	message := strings.Join(lines, "\n")
	var emoji string
	switch {
	case daily:
		emoji = "📊"
	case weekly:
		emoji = "📈"
	case monthly:
		emoji = "📅"
	}

	if err := messageSender.SendEvent(models.EventMessage{
		Event:   eventType,
		Clients: eventClients,
		Time:    now,
		Emoji:   emoji,
		Message: message,
	}); err != nil {
		return result, fmt.Errorf("send %s traffic report: %w", label, err)
	}
	result.Sent = true
	result.ClientCount = len(eventClients)
	return result, nil
}

func currentDailyTrafficReportRange(now time.Time) (time.Time, time.Time) {
	beijingNow := now.In(beijingLocation)
	start := time.Date(beijingNow.Year(), beijingNow.Month(), beijingNow.Day(), 0, 0, 0, 0, beijingLocation)
	return start.UTC(), now.UTC()
}

func previousTrafficReportRange(now time.Time, period string) (time.Time, time.Time) {
	localNow := now.In(beijingLocation)
	var startLocal, endLocal time.Time

	switch period {
	case "daily":
		yesterday := localNow.AddDate(0, 0, -1)
		startLocal = time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, beijingLocation)
		endLocal = startLocal.AddDate(0, 0, 1)
	case "weekly":
		weekday := int(localNow.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		lastMonday := localNow.AddDate(0, 0, -(weekday-1)-7)
		startLocal = time.Date(lastMonday.Year(), lastMonday.Month(), lastMonday.Day(), 0, 0, 0, 0, beijingLocation)
		endLocal = startLocal.AddDate(0, 0, 7)
	case "monthly":
		endLocal = time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, beijingLocation)
		startLocal = endLocal.AddDate(0, -1, 0)
	default:
		return time.Time{}, time.Time{}
	}

	return startLocal.UTC(), endLocal.Add(-time.Nanosecond).UTC()
}

type trafficUsage struct {
	Up   int64
	Down int64
}

func formatTrafficReportLine(client models.Client, suffix string, usage trafficUsage, includeTraffic, includeBilling bool) string {
	name := strings.TrimSpace(client.Name)
	if name == "" {
		name = client.UUID
	}
	parts := make([]string, 0, 3)
	if includeTraffic {
		parts = append(parts, "上行 "+humanBytes(usage.Up), "下行 "+humanBytes(usage.Down))
	}
	if includeBilling {
		rule := strings.ToLower(strings.TrimSpace(client.TrafficLimitType))
		switch rule {
		case "up", "down", "sum", "min", "max":
		default:
			rule = "max"
		}
		used := computeUsedByType(rule, usage.Up, usage.Down)
		parts = append(parts, fmt.Sprintf("计费流量 %s（%s）", humanBytes(used), rule))
	}
	return fmt.Sprintf("%s %s：%s", name, suffix, strings.Join(parts, "，"))
}

// getClientTrafficInRange 查询某客户端在指定时间段内的上下行流量增量。
//
// 历史监控数据已完全迁移到 metric store，这里从 metric store 读取区间内记录并
// 累加精确的流量增量字段计算用量；缺失增量时回退到累计流量差值。
func getClientTrafficInRange(clientUUID string, start, end time.Time) (trafficUsage, error) {
	ctx := context.Background()
	recs, err := metricstore.GetRecordsByClientAndTime(ctx, clientUUID, start, end)
	if err != nil {
		return trafficUsage{}, err
	}

	records := make([]trafficDeltaRecord, 0, len(recs))
	for _, r := range recs {
		records = append(records, trafficDeltaRecord{
			Time:         r.Time,
			NetTotalUp:   r.NetTotalUp,
			NetTotalDown: r.NetTotalDown,
			TrafficUp:    r.TrafficUp,
			TrafficDown:  r.TrafficDown,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Time.Before(records[j].Time)
	})

	// 计算增量基线（区间开始前最后一条累计流量）
	var previous *trafficDeltaRecord
	baseline, err := metricstore.GetLatestTrafficBefore(ctx, []string{clientUUID}, start)
	if err != nil {
		return trafficUsage{}, err
	}
	if base, ok := baseline[clientUUID]; ok {
		previous = &trafficDeltaRecord{
			Time:         base.Time,
			NetTotalUp:   base.NetTotalUp,
			NetTotalDown: base.NetTotalDown,
		}
	}

	totalUp, totalDown := sumTrafficDeltas(records, previous)
	return trafficUsage{Up: totalUp, Down: totalDown}, nil
}

type trafficDeltaRecord struct {
	Time         time.Time
	NetTotalUp   int64
	NetTotalDown int64
	TrafficUp    int64
	TrafficDown  int64
}

const (
	trafficCounterRecoveryWindow  = 30 * time.Minute
	trafficDeltaAnomalyMultiplier = int64(4)
	trafficDeltaAnomalyAllowance  = int64(64 * 1024 * 1024)
)

func sumTrafficDeltas(records []trafficDeltaRecord, previous *trafficDeltaRecord) (int64, int64) {
	hasPrevious := previous != nil
	var previousUp int64
	var previousDown int64
	if previous != nil {
		previousUp = previous.NetTotalUp
		previousDown = previous.NetTotalDown
	}

	totalUp := sumTrafficDirection(
		records,
		hasPrevious,
		previousUp,
		func(record trafficDeltaRecord) int64 { return record.NetTotalUp },
		func(record trafficDeltaRecord) int64 { return record.TrafficUp },
	)
	totalDown := sumTrafficDirection(
		records,
		hasPrevious,
		previousDown,
		func(record trafficDeltaRecord) int64 { return record.NetTotalDown },
		func(record trafficDeltaRecord) int64 { return record.TrafficDown },
	)
	return totalUp, totalDown
}

func sumTrafficDirection(
	records []trafficDeltaRecord,
	hasBaseline bool,
	baseline int64,
	totalValue func(trafficDeltaRecord) int64,
	storedDelta func(trafficDeltaRecord) int64,
) int64 {
	var total int64
	for i := 0; i < len(records); i++ {
		current := totalValue(records[i])
		if hasBaseline && current < baseline {
			if recoveryIndex := findTrafficCounterRecovery(records, i+1, baseline, records[i].Time, totalValue); recoveryIndex >= 0 {
				recovered := totalValue(records[recoveryIndex])
				total += recovered - baseline
				baseline = recovered
				i = recoveryIndex
				continue
			}
		}

		delta := storedDelta(records[i])
		if hasBaseline {
			delta = trafficDeltaOrFallback(delta, current, baseline)
			if current >= baseline {
				directDelta := current - baseline
				if delta > trafficDeltaUpperBound(directDelta) {
					delta = directDelta
				}
			}
		}
		total += delta
		baseline = current
		hasBaseline = true
	}
	return total
}

func findTrafficCounterRecovery(
	records []trafficDeltaRecord,
	start int,
	baseline int64,
	dropTime time.Time,
	totalValue func(trafficDeltaRecord) int64,
) int {
	for i := start; i < len(records); i++ {
		if records[i].Time.Sub(dropTime) > trafficCounterRecoveryWindow {
			break
		}
		if totalValue(records[i]) >= baseline {
			return i
		}
	}
	return -1
}

func trafficDeltaUpperBound(directDelta int64) int64 {
	if directDelta > (math.MaxInt64-trafficDeltaAnomalyAllowance)/trafficDeltaAnomalyMultiplier {
		return math.MaxInt64
	}
	return directDelta*trafficDeltaAnomalyMultiplier + trafficDeltaAnomalyAllowance
}

func trafficDeltaOrFallback(storedDelta, currentTotal, previousTotal int64) int64 {
	if storedDelta > 0 {
		return storedDelta
	}
	return metricstore.TrafficCounterDelta(currentTotal, previousTotal)
}
