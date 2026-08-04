package tasks

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
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
}
