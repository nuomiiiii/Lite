package dbcore

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigrateLegacyReturnRouteLines(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:return-route-line-migration?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.ReturnRouteTask{},
		&models.ReturnRouteStatus{},
		&models.ReturnRouteEvent{},
	))

	client := models.Client{UUID: "node-1", Name: "node-1"}
	require.NoError(t, db.Create(&client).Error)
	task := models.ReturnRouteTask{
		Name: "legacy", Client: client.UUID, Carrier: "unicom", Region: "华东",
		Target: "example.com", IPVersion: 4, ExpectedLine: legacyReturnRouteLine10099,
		Protocol: "icmp", Interval: 180, SwitchConfirm: 2, RecoveryConfirm: 3,
	}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&models.ReturnRouteStatus{
		TaskId: task.Id, CurrentLine: legacyReturnRouteLine10099,
		CandidateLine: legacyReturnRouteLine10099, State: "healthy",
	}).Error)
	require.NoError(t, db.Create(&models.ReturnRouteEvent{
		TaskId: task.Id, Client: client.UUID, ExpectedLine: legacyReturnRouteLine10099,
		Kind: "switch", FromLine: legacyReturnRouteLine10099, ToLine: legacyReturnRouteLine10099,
		OccurredAt: time.Now().UTC(),
	}).Error)

	require.NoError(t, migrateLegacyReturnRouteLines(db))

	var migratedTask models.ReturnRouteTask
	require.NoError(t, db.First(&migratedTask, task.Id).Error)
	require.Equal(t, returnRouteLineCUGVIP, migratedTask.ExpectedLine)
	var status models.ReturnRouteStatus
	require.NoError(t, db.First(&status, "task_id = ?", task.Id).Error)
	require.Equal(t, returnRouteLineCUGVIP, status.CurrentLine)
	require.Equal(t, returnRouteLineCUGVIP, status.CandidateLine)
	var event models.ReturnRouteEvent
	require.NoError(t, db.First(&event, "task_id = ?", task.Id).Error)
	require.Equal(t, returnRouteLineCUGVIP, event.ExpectedLine)
	require.Equal(t, returnRouteLineCUGVIP, event.FromLine)
	require.Equal(t, returnRouteLineCUGVIP, event.ToLine)
}
