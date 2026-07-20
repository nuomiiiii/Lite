package notifier

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/pkg/corn"
	"github.com/komari-monitor/komari/pkg/metric"
	"github.com/komari-monitor/komari/utils/messageSender"
)

const pingLossCheckInterval = 15 * time.Second

var pingLossCheckMu sync.Mutex

type pingLossStats struct {
	Total int64
	Lost  int64
}

type pingLossNotificationAction uint8

const (
	pingLossNotificationNone pingLossNotificationAction = iota
	pingLossNotificationAlert
	pingLossNotificationRecovery
)

func (stats pingLossStats) LossRate() float64 {
	if stats.Total == 0 {
		return 0
	}
	return float64(stats.Lost) / float64(stats.Total) * 100
}

func InitPingLossNotificationSchedule() {
	if err := corn.AddFunc("ping-loss-notification", corn.Every(pingLossCheckInterval), CheckPingLossNotifications); err != nil {
		log.Printf("Failed to register ping loss notification job: %v", err)
	}
}

func CheckPingLossNotifications() {
	if !pingLossCheckMu.TryLock() {
		return
	}
	defer pingLossCheckMu.Unlock()

	enabled, err := config.GetAs[bool](config.NotificationEnabledKey, false)
	if err != nil || !enabled {
		return
	}

	store := metricstore.GetStore()
	if store == nil {
		log.Printf("Failed to check ping loss notifications: metric store is not initialized")
		return
	}

	db := dbcore.GetDBInstance()
	var notifications []models.PingLossNotification
	if err := db.Preload("ClientInfo").Preload("Task").Where("enable = ?", true).Find(&notifications).Error; err != nil {
		log.Printf("Failed to load ping loss notifications: %v", err)
		return
	}

	now := time.Now().UTC()
	for _, notification := range notifications {
		windowStart := now.Add(-time.Duration(notification.WindowSeconds) * time.Second)
		stats, err := getPingLossStatsWithStore(context.Background(), store, notification.Client, notification.TaskId, windowStart, now)
		if err != nil {
			log.Printf("Failed to compute ping loss for notification %d: %v", notification.Id, err)
			continue
		}
		action := evaluatePingLossNotification(notification, stats, now)
		if action == pingLossNotificationNone {
			continue
		}

		if err := sendPingLossNotification(notification, stats, now, action); err != nil {
			log.Printf("Failed to send ping loss notification %d: %v", notification.Id, err)
			continue
		}
		if err := db.Model(&models.PingLossNotification{}).
			Where("id = ?", notification.Id).
			Updates(map[string]any{
				"last_notified": now,
				"alert_active":  action == pingLossNotificationAlert,
			}).Error; err != nil {
			log.Printf("Failed to update ping loss notification %d: %v", notification.Id, err)
		}
	}
}

func getPingLossStatsWithStore(ctx context.Context, store *metric.Store, clientUUID string, taskID uint, start, end time.Time) (pingLossStats, error) {
	if store == nil {
		return pingLossStats{}, fmt.Errorf("metric store is not initialized")
	}
	interval := store.CompatibleSeriesInterval(start, end, time.Minute)
	points, err := store.Series(ctx, metric.AggregateQuery{
		Query: metric.Query{
			MetricName: metricstore.MetricPingLoss,
			EntityID:   clientUUID,
			Start:      start,
			End:        end,
			Order:      metric.OrderAsc,
			Tags:       map[string]string{"task_id": fmt.Sprintf("%d", taskID)},
		},
		Aggregation:    metric.AggAvg,
		Interval:       interval,
		PreserveSeries: true,
	}, end)
	if err != nil {
		return pingLossStats{}, err
	}
	return pingLossStatsFromPoints(points), nil
}

func pingLossStatsFromPoints(points []metric.AggregatePoint) pingLossStats {
	var stats pingLossStats
	for _, point := range points {
		if point.Count <= 0 {
			continue
		}
		count := int64(point.Count)
		lost := int64(math.Round(point.Value * float64(point.Count)))
		if lost < 0 {
			lost = 0
		} else if lost > count {
			lost = count
		}
		stats.Total += count
		stats.Lost += lost
	}
	return stats
}

func evaluatePingLossNotification(notification models.PingLossNotification, stats pingLossStats, now time.Time) pingLossNotificationAction {
	if !notification.Enable || stats.Total < int64(notification.MinimumSamples) {
		return pingLossNotificationNone
	}
	if stats.LossRate() <= notification.LossThreshold {
		if notification.AlertActive {
			return pingLossNotificationRecovery
		}
		return pingLossNotificationNone
	}
	if notification.AlertActive && notification.LastNotified != nil && now.Before(notification.LastNotified.Add(time.Duration(notification.CooldownSeconds)*time.Second)) {
		return pingLossNotificationNone
	}
	return pingLossNotificationAlert
}

func formatPingLossMessage(notification models.PingLossNotification, stats pingLossStats, action pingLossNotificationAction) string {
	clientName := strings.TrimSpace(notification.ClientInfo.Name)
	if clientName == "" {
		clientName = notification.Client
	}
	taskName := strings.TrimSpace(notification.Task.Name)
	if taskName == "" {
		taskName = "未命名任务"
	}
	target := strings.TrimSpace(notification.Task.Target)
	if target == "" {
		target = "-"
	}
	heading := "延迟监测异常"
	if action == pingLossNotificationRecovery {
		heading = "延迟监测恢复"
	}
	return fmt.Sprintf(
		"%s\n服务器：%s\n检测任务：%s\n检测目标：%s\n统计窗口：最近 %s\n丢包：%.2f%%（%d/%d）\n告警阈值：%.2f%%",
		heading,
		clientName,
		taskName,
		target,
		formatPingLossWindow(notification.WindowSeconds),
		stats.LossRate(),
		stats.Lost,
		stats.Total,
		notification.LossThreshold,
	)
}

func sendPingLossNotification(notification models.PingLossNotification, stats pingLossStats, now time.Time, action pingLossNotificationAction) error {
	client := notification.ClientInfo
	if client.UUID == "" {
		client.UUID = notification.Client
	}
	emoji := "⚠️"
	if action == pingLossNotificationRecovery {
		emoji = "✅"
	}
	return messageSender.SendEvent(models.EventMessage{
		Event:   messageevent.PingLoss,
		Clients: []models.Client{client},
		Time:    now,
		Emoji:   emoji,
		Message: formatPingLossMessage(notification, stats, action),
	})
}

func formatPingLossWindow(seconds int) string {
	if seconds%3600 == 0 {
		return fmt.Sprintf("%d 小时", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%d 分钟", seconds/60)
	}
	return fmt.Sprintf("%d 秒", seconds)
}
