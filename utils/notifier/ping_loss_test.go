package notifier

import (
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/pkg/metric"
	"github.com/stretchr/testify/assert"
)

func TestPingLossStatsFromPoints(t *testing.T) {
	stats := pingLossStatsFromPoints([]metric.AggregatePoint{
		{Value: 0.1, Count: 20},
		{Value: 0.25, Count: 4},
	})
	assert.Equal(t, int64(24), stats.Total)
	assert.Equal(t, int64(3), stats.Lost)
	assert.InDelta(t, 12.5, stats.LossRate(), 0.001)
}

func TestEvaluatePingLossNotification(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	notification := models.PingLossNotification{
		Enable:          true,
		LossThreshold:   5,
		MinimumSamples:  10,
		CooldownSeconds: 300,
	}
	stats := pingLossStats{Total: 20, Lost: 2}
	assert.Equal(t, pingLossNotificationAlert, evaluatePingLossNotification(notification, stats, now))

	lastNotified := now.Add(-time.Minute)
	notification.LastNotified = &lastNotified
	assert.Equal(t, pingLossNotificationAlert, evaluatePingLossNotification(notification, stats, now), "a new alert must not be delayed by stale notification history")

	notification.AlertActive = true
	assert.Equal(t, pingLossNotificationNone, evaluatePingLossNotification(notification, stats, now))

	lastNotified = now.Add(-10 * time.Minute)
	assert.Equal(t, pingLossNotificationAlert, evaluatePingLossNotification(notification, stats, now))

	stats = pingLossStats{Total: 9, Lost: 9}
	assert.Equal(t, pingLossNotificationNone, evaluatePingLossNotification(notification, stats, now))

	stats = pingLossStats{Total: 20, Lost: 1}
	assert.Equal(t, pingLossNotificationRecovery, evaluatePingLossNotification(notification, stats, now))

	notification.AlertActive = false
	assert.Equal(t, pingLossNotificationNone, evaluatePingLossNotification(notification, stats, now))
}

func TestFormatPingLossMessageIdentifiesExactTask(t *testing.T) {
	notification := models.PingLossNotification{
		Client:          "node-a",
		ClientInfo:      models.Client{Name: "东京节点"},
		TaskId:          17,
		Task:            models.PingTask{Name: "Cloudflare DNS", Target: "1.1.1.1"},
		WindowSeconds:   60,
		LossThreshold:   5,
		MinimumSamples:  1,
		CooldownSeconds: 300,
	}
	message := formatPingLossMessage(notification, pingLossStats{Total: 20, Lost: 2}, pingLossNotificationAlert)
	for _, expected := range []string{"延迟监测异常", "东京节点", "检测任务：Cloudflare DNS", "1.1.1.1", "10.00%", "2/20"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message %q does not contain %q", message, expected)
		}
	}
	assert.NotContains(t, message, "(#17)")
	assert.Equal(t, "延迟监测告警", messageevent.PingLoss)
}

func TestFormatPingLossRecoveryMessage(t *testing.T) {
	notification := models.PingLossNotification{
		Client:        "node-a",
		ClientInfo:    models.Client{Name: "宁波服务器"},
		TaskId:        117,
		Task:          models.PingTask{Name: "宁波电信", Target: "example.com"},
		WindowSeconds: 60,
		LossThreshold: 5,
	}
	message := formatPingLossMessage(notification, pingLossStats{Total: 20, Lost: 1}, pingLossNotificationRecovery)
	assert.Contains(t, message, "延迟监测恢复")
	assert.Contains(t, message, "检测任务：宁波电信")
	assert.NotContains(t, message, "#117")
}

func TestFormatPingLossWindow(t *testing.T) {
	assert.Equal(t, "1 分钟", formatPingLossWindow(60))
	assert.Equal(t, "2 小时", formatPingLossWindow(7200))
	assert.Equal(t, "90 秒", formatPingLossWindow(90))
}
