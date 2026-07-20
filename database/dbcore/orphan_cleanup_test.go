package dbcore

import (
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCleanupOrphanedPingLossNotifications(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:ping-loss-orphan-cleanup?mode=memory&cache=shared&_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a"}).Error)
	task := models.PingTask{Name: "DNS", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "1.1.1.1", Interval: 10}
	require.NoError(t, db.Create(&task).Error)

	require.NoError(t, db.Create([]models.PingLossNotification{
		{Client: "client-a", TaskId: task.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "missing-client", TaskId: task.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-a", TaskId: task.Id + 100, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}).Error)

	require.NoError(t, cleanupOrphanedPingLossNotifications(db))
	var notifications []models.PingLossNotification
	require.NoError(t, db.Find(&notifications).Error)
	require.Len(t, notifications, 1)
	assert.Equal(t, "client-a", notifications[0].Client)
	assert.Equal(t, task.Id, notifications[0].TaskId)
}
