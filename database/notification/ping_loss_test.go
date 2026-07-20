package notification

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestValidatePingLossNotification(t *testing.T) {
	valid := models.PingLossNotification{
		Client:          "client-a",
		TaskId:          1,
		WindowSeconds:   60,
		LossThreshold:   5,
		MinimumSamples:  1,
		CooldownSeconds: 300,
	}
	assert.NoError(t, ValidatePingLossNotification(valid))

	tests := []struct {
		name   string
		mutate func(*models.PingLossNotification)
	}{
		{name: "missing client", mutate: func(n *models.PingLossNotification) { n.Client = "" }},
		{name: "missing task", mutate: func(n *models.PingLossNotification) { n.TaskId = 0 }},
		{name: "short window", mutate: func(n *models.PingLossNotification) { n.WindowSeconds = 59 }},
		{name: "invalid threshold", mutate: func(n *models.PingLossNotification) { n.LossThreshold = 0 }},
		{name: "invalid samples", mutate: func(n *models.PingLossNotification) { n.MinimumSamples = 0 }},
		{name: "short cooldown", mutate: func(n *models.PingLossNotification) { n.CooldownSeconds = 59 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			assert.Error(t, ValidatePingLossNotification(candidate))
		})
	}
}

func TestUpsertPingLossNotificationsCreatesAndUpdatesTargets(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ping-loss-upsert?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a", Name: "Server A"}).Error)
	task := models.PingTask{
		Name:     "Public DNS",
		Clients:  models.StringArray{"client-a"},
		Type:     "icmp",
		Target:   "1.1.1.1",
		Interval: 10,
	}
	require.NoError(t, db.Create(&task).Error)

	first := &models.PingLossNotification{
		Client:          "client-a",
		TaskId:          task.Id,
		Enable:          true,
		WindowSeconds:   60,
		LossThreshold:   5,
		MinimumSamples:  3,
		CooldownSeconds: 300,
	}
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{first}))

	var created models.PingLossNotification
	require.NoError(t, db.First(&created).Error)
	require.NotZero(t, created.Id)
	assert.True(t, created.Enable)
	assert.Equal(t, 5.0, created.LossThreshold)

	lastNotified := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, db.Model(&created).Updates(map[string]any{
		"last_notified": lastNotified,
		"alert_active":  true,
	}).Error)
	updated := &models.PingLossNotification{
		Client:          "client-a",
		TaskId:          task.Id,
		Enable:          false,
		WindowSeconds:   120,
		LossThreshold:   12.5,
		MinimumSamples:  8,
		CooldownSeconds: 600,
	}
	require.NoError(t, upsertPingLossNotifications(db, []*models.PingLossNotification{updated}))

	var count int64
	require.NoError(t, db.Model(&models.PingLossNotification{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var got models.PingLossNotification
	require.NoError(t, db.First(&got).Error)
	assert.Equal(t, created.Id, got.Id)
	assert.False(t, got.Enable)
	assert.Equal(t, 120, got.WindowSeconds)
	assert.Equal(t, 12.5, got.LossThreshold)
	assert.Equal(t, 8, got.MinimumSamples)
	assert.Equal(t, 600, got.CooldownSeconds)
	require.NotNil(t, got.LastNotified)
	assert.WithinDuration(t, lastNotified, *got.LastNotified, time.Second)
	assert.True(t, got.AlertActive)
}

func TestUpsertPingLossNotificationsRollsBackBatch(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ping-loss-rollback?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-rollback"}).Error)
	task := models.PingTask{
		Name: "DNS", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "8.8.8.8", Interval: 10,
	}
	require.NoError(t, db.Create(&task).Error)

	valid := &models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true,
		WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
	}
	invalid := &models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true,
		WindowSeconds: 30, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300,
	}
	require.Error(t, upsertPingLossNotifications(db, []*models.PingLossNotification{valid, invalid}))

	var count int64
	require.NoError(t, db.Model(&models.PingLossNotification{}).Count(&count).Error)
	assert.Zero(t, count)
}
