package tasks

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newPingTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:ping-order-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.PingTask{}))
	return db
}

func TestGetPingTasksByClientUsesStableWeightOrder(t *testing.T) {
	db := newPingTaskTestDB(t)
	tasks := []models.PingTask{
		{Name: "late", Weight: 20, Clients: models.StringArray{"node-a"}, Type: "icmp", Target: "late.example", Interval: 10},
		{Name: "first-tie", Weight: 10, Clients: models.StringArray{"node-a"}, Type: "icmp", Target: "a.example", Interval: 10},
		{Name: "second-tie", Weight: 10, Clients: models.StringArray{"node-a"}, Type: "icmp", Target: "b.example", Interval: 10},
		{Name: "other-node", Weight: 0, Clients: models.StringArray{"node-b"}, Type: "icmp", Target: "other.example", Interval: 10},
	}
	require.NoError(t, db.Create(&tasks).Error)

	got, err := getPingTasksByClient(db, "node-a")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []uint{tasks[1].Id, tasks[2].Id, tasks[0].Id}, []uint{got[0].Id, got[1].Id, got[2].Id})
}

func TestUpdatePingTaskOrderValidatesBatchBeforeWriting(t *testing.T) {
	db := newPingTaskTestDB(t)
	tasks := []models.PingTask{
		{Name: "a", Weight: 1, Type: "icmp", Target: "a.example", Interval: 10},
		{Name: "b", Weight: 2, Type: "icmp", Target: "b.example", Interval: 10},
	}
	require.NoError(t, db.Create(&tasks).Error)

	err := updatePingTaskOrder(db, map[uint]int{tasks[0].Id: 10, 999_999: 20})
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "error = %v", err)
	var unchanged []models.PingTask
	require.NoError(t, db.Order("id ASC").Find(&unchanged).Error)
	assert.Equal(t, []int{1, 2}, []int{unchanged[0].Weight, unchanged[1].Weight})

	require.NoError(t, updatePingTaskOrder(db, map[uint]int{tasks[0].Id: 1, tasks[1].Id: 2}), "unchanged weights must remain valid")
	require.NoError(t, updatePingTaskOrder(db, map[uint]int{tasks[0].Id: 30, tasks[1].Id: 20}))
	var updated []models.PingTask
	require.NoError(t, db.Order("id ASC").Find(&updated).Error)
	assert.Equal(t, []int{30, 20}, []int{updated[0].Weight, updated[1].Weight})
}

func TestUpdatePingTaskOrderAcceptsEmptyBatchWithoutDatabase(t *testing.T) {
	require.NoError(t, updatePingTaskOrder(nil, nil))
}

func TestSavePingRecordRejectsLateReportForRemovedAssignment(t *testing.T) {
	task := models.PingTask{
		Id: 7, Clients: models.StringArray{"client-a"}, Interval: 60,
	}
	require.NoError(t, utils.ReloadPingSchedule([]models.PingTask{task}))
	t.Cleanup(func() { require.NoError(t, utils.ReloadPingSchedule(nil)) })

	err := SavePingRecord(models.PingRecord{
		Client: "client-b", TaskId: task.Id, Time: time.Now().UTC(), Value: 12,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not assigned")
}

func TestDeletePingTaskRowsCleansMatchingPingLossNotifications(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delete-ping-task-cleanup.db")+"?_foreign_keys=off"), &gorm.Config{
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
		&models.MetricCleanupJob{},
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
	require.NoError(t, db.Exec("CREATE TABLE ping_records (client TEXT NOT NULL, task_id INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO ping_records (client, task_id) VALUES (?, ?), (?, ?)",
		"client-a", tasks[0].Id, "client-a", tasks[1].Id).Error)

	require.NoError(t, deletePingTaskRows(db, []uint{tasks[0].Id}))

	var remainingTasks []models.PingTask
	require.NoError(t, db.Order("id ASC").Find(&remainingTasks).Error)
	require.Len(t, remainingTasks, 1)
	assert.Equal(t, tasks[1].Id, remainingTasks[0].Id)

	var remainingNotifications []models.PingLossNotification
	require.NoError(t, db.Order("id ASC").Find(&remainingNotifications).Error)
	require.Len(t, remainingNotifications, 1)
	assert.Equal(t, tasks[1].Id, remainingNotifications[0].TaskId)
	var legacyCount int64
	require.NoError(t, db.Table("ping_records").Where("task_id = ?", tasks[0].Id).Count(&legacyCount).Error)
	assert.Zero(t, legacyCount)
	require.NoError(t, db.Table("ping_records").Where("task_id = ?", tasks[1].Id).Count(&legacyCount).Error)
	assert.Equal(t, int64(1), legacyCount)
	var cleanupJobs []models.MetricCleanupJob
	require.NoError(t, db.Find(&cleanupJobs).Error)
	require.Len(t, cleanupJobs, 1)
	assert.Equal(t, models.MetricCleanupPingTask, cleanupJobs[0].Kind)
	assert.Equal(t, tasks[0].Id, cleanupJobs[0].TaskID)
}

func TestEditPingTasksRemovesAlertsForUnassignedClients(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "edit-ping-task-alert-cleanup.db")+"?_foreign_keys=off"), &gorm.Config{
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
		&models.MetricCleanupJob{},
	))
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-a", Token: "token-a", Name: "Server A"},
		{UUID: "client-b", Token: "token-b", Name: "Server B"},
		{UUID: "client-c", Token: "token-c", Name: "Server C"},
	}).Error)

	tasks := []models.PingTask{
		{Name: "Task 1", Clients: models.StringArray{"client-a", "client-b", "client-c"}, Type: "icmp", Target: "1.1.1.1", Interval: 60},
		{Name: "Task 2", Clients: models.StringArray{"client-b"}, Type: "icmp", Target: "8.8.8.8", Interval: 60},
	}
	require.NoError(t, db.Create(&tasks).Error)
	require.NoError(t, db.Create([]models.PingLossNotification{
		{Client: "client-a", TaskId: tasks[0].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-b", TaskId: tasks[0].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-c", TaskId: tasks[0].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-b", TaskId: tasks[1].Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}).Error)
	require.NoError(t, db.Exec("CREATE TABLE ping_records (client TEXT NOT NULL, task_id INTEGER NOT NULL)").Error)
	require.NoError(t, db.Exec("INSERT INTO ping_records (client, task_id) VALUES (?, ?), (?, ?), (?, ?), (?, ?)",
		"client-a", tasks[0].Id,
		"client-b", tasks[0].Id,
		"client-c", tasks[0].Id,
		"client-b", tasks[1].Id,
	).Error)

	updated := tasks[0]
	updated.Clients = models.StringArray{"client-a", "client-c"}
	removed, err := editPingTasks(db, []*models.PingTask{&updated})
	require.NoError(t, err)
	assert.Equal(t, []metricstore.PingAssignment{{Client: "client-b", TaskID: tasks[0].Id}}, removed)
	require.Len(t, removed, 1)
	assert.True(t, metricstore.PingAssignmentWritesBlocked(removed[0]))
	t.Cleanup(func() { metricstore.UnblockPingAssignmentWrites(removed) })

	var gotTask models.PingTask
	require.NoError(t, db.First(&gotTask, tasks[0].Id).Error)
	assert.Equal(t, models.StringArray{"client-a", "client-c"}, gotTask.Clients)

	var remaining []models.PingLossNotification
	require.NoError(t, db.Order("task_id ASC").Order("client ASC").Find(&remaining).Error)
	require.Len(t, remaining, 3)
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
		{taskID: remaining[0].TaskId, client: remaining[0].Client},
		{taskID: remaining[1].TaskId, client: remaining[1].Client},
		{taskID: remaining[2].TaskId, client: remaining[2].Client},
	})
	var removedLegacyCount int64
	require.NoError(t, db.Table("ping_records").Where("client = ? AND task_id = ?", "client-b", tasks[0].Id).Count(&removedLegacyCount).Error)
	assert.Zero(t, removedLegacyCount)
	var retainedLegacyCount int64
	require.NoError(t, db.Table("ping_records").Count(&retainedLegacyCount).Error)
	assert.Equal(t, int64(3), retainedLegacyCount)
	var cleanupJobs []models.MetricCleanupJob
	require.NoError(t, db.Find(&cleanupJobs).Error)
	require.Len(t, cleanupJobs, 1)
	assert.Equal(t, models.MetricCleanupPingAssignment, cleanupJobs[0].Kind)
	assert.Equal(t, "client-b", cleanupJobs[0].Client)
	assert.Equal(t, tasks[0].Id, cleanupJobs[0].TaskID)
}
