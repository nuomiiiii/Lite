package notifier

import (
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
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

func TestShouldNotifyPingLoss(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	notification := models.PingLossNotification{
		Enable:          true,
		LossThreshold:   5,
		MinimumSamples:  10,
		CooldownSeconds: 300,
	}
	stats := pingLossStats{Total: 20, Lost: 2}
	assert.True(t, shouldNotifyPingLoss(notification, stats, now))

	lastNotified := now.Add(-time.Minute)
	notification.LastNotified = &lastNotified
	assert.False(t, shouldNotifyPingLoss(notification, stats, now))

	lastNotified = now.Add(-10 * time.Minute)
	assert.True(t, shouldNotifyPingLoss(notification, stats, now))

	stats = pingLossStats{Total: 9, Lost: 9}
	assert.False(t, shouldNotifyPingLoss(notification, stats, now))

	stats = pingLossStats{Total: 20, Lost: 1}
	assert.False(t, shouldNotifyPingLoss(notification, stats, now))
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
	message := formatPingLossMessage(notification, pingLossStats{Total: 20, Lost: 2})
	for _, expected := range []string{"东京节点", "Cloudflare DNS (#17)", "1.1.1.1", "10.00%", "2/20"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message %q does not contain %q", message, expected)
		}
	}
}

func TestFormatPingLossWindow(t *testing.T) {
	assert.Equal(t, "1 分钟", formatPingLossWindow(60))
	assert.Equal(t, "2 小时", formatPingLossWindow(7200))
	assert.Equal(t, "90 秒", formatPingLossWindow(90))
}
