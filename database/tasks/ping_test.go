package tasks

import (
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDeletePingTaskRowsCleansMatchingPingLossNotifications(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:delete-ping-task-cleanup?mode=memory&cache=shared&_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create(&models.Client{
		UUID: "client-a", Token: "token-a", Name: "Server A",
	}).Error)

	tasks := []models.PingTask{
		{Name: "Target C", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "c.example.com", Interval: 10},
		{Name: "Target D", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "d.example.com", Interval: 10},
	}
	require.NoError(t, db.Create(&tasks).Error)
	require.NoError(t, db.Create([]models.PingLossNotification{
		{Client: "client-a", TaskId: tasks[0].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-a", TaskId: tasks[1].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}).Error)

	require.NoError(t, deletePingTaskRows(db, []uint{tasks[0].Id}))

	var remainingTasks []models.PingTask
	require.NoError(t, db.Order("id ASC").Find(&remainingTasks).Error)
	require.Len(t, remainingTasks, 1)
	assert.Equal(t, tasks[1].Id, remainingTasks[0].Id)

	var remainingNotifications []models.PingLossNotification
	require.NoError(t, db.Order("id ASC").Find(&remainingNotifications).Error)
	require.Len(t, remainingNotifications, 1)
	assert.Equal(t, tasks[1].Id, remainingNotifications[0].TaskId)
}
