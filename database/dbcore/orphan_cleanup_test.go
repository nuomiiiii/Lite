package dbcore

import (
	"path/filepath"
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCleanupOrphanedPingLossNotifications(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ping-loss-orphan-cleanup.db")+"?_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-a", Token: "token-a"},
		{UUID: "client-b", Token: "token-b"},
	}).Error)
	task := models.PingTask{Name: "DNS", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "1.1.1.1", Interval: 10}
	require.NoError(t, db.Create(&task).Error)
	otherTask := models.PingTask{Name: "Backup DNS", Clients: models.StringArray{"client-b"}, Type: "icmp", Target: "8.8.8.8", Interval: 10}
	require.NoError(t, db.Create(&otherTask).Error)

	require.NoError(t, db.Create([]models.PingLossNotification{
		{Client: "client-a", TaskId: task.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-b", TaskId: task.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-b", TaskId: otherTask.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "missing-client", TaskId: task.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-a", TaskId: task.Id + 100, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}).Error)

	require.NoError(t, cleanupOrphanedPingLossNotifications(db))
	var notifications []models.PingLossNotification
	require.NoError(t, db.Order("task_id ASC").Find(&notifications).Error)
	require.Len(t, notifications, 2)
	assert.Equal(t, "client-a", notifications[0].Client)
	assert.Equal(t, task.Id, notifications[0].TaskId)
	assert.Equal(t, "client-b", notifications[1].Client)
	assert.Equal(t, otherTask.Id, notifications[1].TaskId)
}

func TestCleanupOrphanedClientDataReconcilesHistoricalPingLossAssignments(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "historical-ping-loss-assignment-cleanup.db")+"?_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
		&models.LoadNotification{},
		&models.Task{},
		&models.TaskResult{},
	))
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-a", Token: "token-a"},
		{UUID: "client-b", Token: "token-b"},
		{UUID: "client-c", Token: "token-c"},
	}).Error)
	tasks := []models.PingTask{
		{Name: "Task 1", Clients: models.StringArray{"client-a", "client-c"}, Type: "icmp", Target: "1.1.1.1", Interval: 60},
		{Name: "Task 2", Clients: models.StringArray{"client-b"}, Type: "icmp", Target: "8.8.8.8", Interval: 60},
	}
	require.NoError(t, db.Create(&tasks).Error)
	require.NoError(t, db.Create([]models.PingLossNotification{
		{Client: "client-a", TaskId: tasks[0].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-b", TaskId: tasks[0].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-c", TaskId: tasks[0].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-b", TaskId: tasks[1].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}).Error)

	assertAssignments := func() {
		t.Helper()
		var notifications []models.PingLossNotification
		require.NoError(t, db.Order("task_id ASC").Order("client ASC").Find(&notifications).Error)
		require.Len(t, notifications, 3)
		assert.Equal(t, []struct {
			taskID uint
			client string
		}{
			{taskID: tasks[0].Id, client: "client-a"},
			{taskID: tasks[0].Id, client: "client-c"},
			{taskID: tasks[1].Id, client: "client-b"},
		}, []struct {
			taskID uint
			client string
		}{
			{taskID: notifications[0].TaskId, client: notifications[0].Client},
			{taskID: notifications[1].TaskId, client: notifications[1].Client},
			{taskID: notifications[2].TaskId, client: notifications[2].Client},
		})
	}

	require.NoError(t, cleanupOrphanedClientData(db))
	assertAssignments()
	require.NoError(t, cleanupOrphanedClientData(db))
	assertAssignments()
}

func TestCleanupOrphanedClientDataRepairsAllAssociations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:full-client-orphan-cleanup?mode=memory&cache=shared&_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
		&models.OfflineNotification{},
		&models.TrafficReportNotification{},
		&models.TrafficDailyLedger{},
		&models.LoadNotification{},
		&models.LoadNotificationState{},
		&models.Task{},
		&models.TaskResult{},
	))
	for _, table := range []string{"records", "records_long_term", "gpu_records", "ping_records"} {
		require.NoError(t, db.Exec("CREATE TABLE "+table+" (client TEXT NOT NULL, task_id INTEGER)").Error)
		require.NoError(t, db.Exec("INSERT INTO "+table+" (client, task_id) VALUES (?, ?), (?, ?)",
			"client-a", 1, "deleted-client", 999).Error)
	}
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-a", Token: "token-a"},
		{UUID: "client-b", Token: "token-b"},
	}).Error)
	pingTask := models.PingTask{
		Name: "DNS", Clients: models.StringArray{"client-a", "deleted-client"},
		Type: "icmp", Target: "1.1.1.1", Interval: 10,
	}
	require.NoError(t, db.Create(&pingTask).Error)
	require.NoError(t, db.Create([]models.PingLossNotification{
		{Client: "client-a", TaskId: pingTask.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "deleted-client", TaskId: pingTask.Id, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}).Error)
	require.NoError(t, db.Create([]models.OfflineNotification{{Client: "client-a"}, {Client: "deleted-client"}}).Error)
	require.NoError(t, db.Create([]models.TrafficReportNotification{{Client: "client-a"}, {Client: "deleted-client"}}).Error)
	require.NoError(t, db.Create([]models.TrafficDailyLedger{
		{Client: "client-a", Day: "2026-07-24"},
		{Client: "deleted-client", Day: "2026-07-24"},
	}).Error)
	loadRules := []models.LoadNotification{
		{Name: "shared", Clients: models.StringArray{"client-a", "deleted-client"}, Metric: "cpu", Interval: 15},
		{Name: "orphan", Clients: models.StringArray{"deleted-client"}, Metric: "cpu", Interval: 15},
	}
	require.NoError(t, db.Create(&loadRules).Error)
	require.NoError(t, db.Create([]models.LoadNotificationState{
		{NotificationID: loadRules[0].Id, Client: "client-a", AlertActive: true},
		{NotificationID: loadRules[0].Id, Client: "deleted-client", AlertActive: true},
		{NotificationID: loadRules[1].Id, Client: "deleted-client", AlertActive: true},
	}).Error)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: loadRules[0].Id, Client: "client-b", AlertActive: true,
	}).Error)
	commandTasks := []models.Task{
		{TaskId: "shared", Clients: models.StringArray{"client-a", "deleted-client"}, Command: "uptime"},
		{TaskId: "orphan", Clients: models.StringArray{"deleted-client"}, Command: "uptime"},
	}
	require.NoError(t, db.Create(&commandTasks).Error)
	require.NoError(t, db.Create([]models.TaskResult{
		{TaskId: "shared", Client: "client-a"},
		{TaskId: "shared", Client: "deleted-client"},
		{TaskId: "missing-task", Client: "client-a"},
	}).Error)

	require.NoError(t, cleanupOrphanedClientData(db))

	var gotPingTask models.PingTask
	require.NoError(t, db.First(&gotPingTask, pingTask.Id).Error)
	assert.Equal(t, models.StringArray{"client-a"}, gotPingTask.Clients)
	var loadNotifications []models.LoadNotification
	require.NoError(t, db.Find(&loadNotifications).Error)
	require.Len(t, loadNotifications, 1)
	assert.Equal(t, models.StringArray{"client-a"}, loadNotifications[0].Clients)
	var loadStates []models.LoadNotificationState
	require.NoError(t, db.Find(&loadStates).Error)
	require.Len(t, loadStates, 1)
	assert.Equal(t, loadRules[0].Id, loadStates[0].NotificationID)
	assert.Equal(t, "client-a", loadStates[0].Client)
	var tasks []models.Task
	require.NoError(t, db.Find(&tasks).Error)
	require.Len(t, tasks, 1)
	assert.Equal(t, "shared", tasks[0].TaskId)
	assert.Equal(t, models.StringArray{"client-a"}, tasks[0].Clients)
	for _, model := range []any{
		&models.PingLossNotification{}, &models.OfflineNotification{},
		&models.TrafficReportNotification{}, &models.TrafficDailyLedger{}, &models.TaskResult{},
	} {
		var count int64
		require.NoError(t, db.Model(model).Count(&count).Error)
		assert.Equal(t, int64(1), count)
	}
	for _, table := range []string{"records", "records_long_term", "gpu_records", "ping_records"} {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		assert.Equal(t, int64(1), count, table)
	}
}
