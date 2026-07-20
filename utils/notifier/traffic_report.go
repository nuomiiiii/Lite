package notifier

import (
	"context"
	"fmt"
	"log"
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
	"github.com/komari-monitor/komari/utils/messageSender"
)

// InitTrafficReportSchedule 注册三个定时任务：日报、周报、月报
func InitTrafficReportSchedule() {
	// 日报：每天凌晨 0 点
	if err := corn.AddFunc("traffic-report-daily", "0 0 0 * * *", func() {
		sendTrafficReport(true, false, false)
	}); err != nil {
		log.Println("Failed to register daily traffic report job:", err)
	}

	// 周报：每周一凌晨 0 点 (dow=1)
	if err := corn.AddFunc("traffic-report-weekly", "0 0 0 * * 1", func() {
		sendTrafficReport(false, true, false)
	}); err != nil {
		log.Println("Failed to register weekly traffic report job:", err)
	}

	// 月报：每月 1 日凌晨 0 点
	if err := corn.AddFunc("traffic-report-monthly", "0 0 0 1 * *", func() {
		sendTrafficReport(false, false, true)
	}); err != nil {
		log.Println("Failed to register monthly traffic report job:", err)
	}

	log.Println("Traffic report schedules registered: daily, weekly, monthly")
}

// sendTrafficReport 汇聚所有启用了指定报告类型的服务器流量，合并成一条通知发送
func sendTrafficReport(daily, weekly, monthly bool) {
	// 检查全局通知开关
	enabled, err := config.GetAs[bool](config.NotificationEnabledKey, false)
	if err != nil || !enabled {
		return
	}

	db := dbcore.GetDBInstance()
	now := time.Now().UTC()

	var eventType, label, suffix string

	switch {
	case daily:
		eventType = messageevent.DReport
		label = "daily"
		suffix = "昨日流量"
	case weekly:
		eventType = messageevent.WReport
		label = "weekly"
		suffix = "上周流量"
	case monthly:
		eventType = messageevent.MReport
		label = "monthly"
		suffix = "上个月流量"
	default:
		return
	}
	start, end := previousTrafficReportRange(now, label)

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
		log.Printf("Failed to query traffic report notifications (%s): %v", label, err)
		return
	}
	if len(notifications) == 0 {
		return
	}

	// 获取客户端信息
	clientUUIDs := make([]string, 0, len(notifications))
	for _, n := range notifications {
		clientUUIDs = append(clientUUIDs, n.Client)
	}
	var clientList []models.Client
	if err := db.Where("uuid IN ?", clientUUIDs).Find(&clientList).Error; err != nil {
		log.Printf("Failed to query clients for traffic report (%s): %v", label, err)
		return
	}
	clientMap := make(map[string]models.Client, len(clientList))
	for _, c := range clientList {
		clientMap[c.UUID] = c
	}

	// 为每个服务器统计流量并拼接消息
	var lines []string
	eventClients := make([]models.Client, 0, len(notifications))
	for _, n := range notifications {
		c, ok := clientMap[n.Client]
		if !ok {
			continue
		}

		usage, err := getClientTrafficInRange(n.Client, start, end)
		if err != nil {
			log.Printf("Failed to compute traffic for client %s (%s): %v", n.Client, label, err)
			continue
		}

		lines = append(lines, formatTrafficReportLine(c, suffix, usage))
		eventClients = append(eventClients, c)
	}

	if len(lines) == 0 {
		return
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
		log.Printf("Failed to send %s traffic report: %v", label, err)
	}
}

func previousTrafficReportRange(now time.Time, period string) (time.Time, time.Time) {
	localNow := now.In(time.Local)
	var startLocal, endLocal time.Time

	switch period {
	case "daily":
		yesterday := localNow.AddDate(0, 0, -1)
		startLocal = time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.Local)
		endLocal = startLocal.AddDate(0, 0, 1)
	case "weekly":
		weekday := int(localNow.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		lastMonday := localNow.AddDate(0, 0, -(weekday-1)-7)
		startLocal = time.Date(lastMonday.Year(), lastMonday.Month(), lastMonday.Day(), 0, 0, 0, 0, time.Local)
		endLocal = startLocal.AddDate(0, 0, 7)
	case "monthly":
		endLocal = time.Date(localNow.Year(), localNow.Month(), 1, 0, 0, 0, 0, time.Local)
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

func formatTrafficReportLine(client models.Client, suffix string, usage trafficUsage) string {
	name := strings.TrimSpace(client.Name)
	if name == "" {
		name = client.UUID
	}
	return fmt.Sprintf("%s %s：上行 %s，下行 %s", name, suffix, humanBytes(usage.Up), humanBytes(usage.Down))
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
