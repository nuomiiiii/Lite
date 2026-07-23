package clients

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGetClientBasicInfoUsesConfiguredOrder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:client-configured-order?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))

	createdAt := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-c", Token: "token-c", Name: "C", Weight: 20, CreatedAt: createdAt},
		{UUID: "client-b", Token: "token-b", Name: "B", Weight: 10, CreatedAt: createdAt.Add(time.Minute)},
		{UUID: "client-a", Token: "token-a", Name: "A", Weight: 10, CreatedAt: createdAt},
	}).Error)

	clients, err := getClientBasicInfo(db)
	require.NoError(t, err)
	require.Len(t, clients, 3)
	assert.Equal(t, []string{"client-a", "client-b", "client-c"}, []string{
		clients[0].UUID, clients[1].UUID, clients[2].UUID,
	})
}

func TestDeleteClientCleansPingLossNotificationsAndTaskAssignments(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:delete-client-cleanup?mode=memory&cache=shared&_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.PingLossNotification{},
	))
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-a", Token: "token-a", Name: "Server A"},
		{UUID: "client-b", Token: "token-b", Name: "Server B"},
	}).Error)

	task := models.PingTask{
		Name: "Public DNS", Clients: models.StringArray{"client-a", "client-b"},
		Type: "icmp", Target: "1.1.1.1", Interval: 10,
	}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create([]models.PingLossNotification{
		{Client: "client-a", TaskId: task.Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
		{Client: "client-b", TaskId: task.Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
	}).Error)

	changed, err := deleteClient(db, "client-a")
	require.NoError(t, err)
	assert.True(t, changed)

	var clientCount int64
	require.NoError(t, db.Model(&models.Client{}).Where("uuid = ?", "client-a").Count(&clientCount).Error)
	assert.Zero(t, clientCount)

	var notifications []models.PingLossNotification
	require.NoError(t, db.Order("client ASC").Find(&notifications).Error)
	require.Len(t, notifications, 1)
	assert.Equal(t, "client-b", notifications[0].Client)

	var gotTask models.PingTask
	require.NoError(t, db.First(&gotTask, task.Id).Error)
	assert.Equal(t, models.StringArray{"client-b"}, gotTask.Clients)
}

func TestNormalizeTrafficResetDay(t *testing.T) {
	for _, value := range []interface{}{float64(0), float64(1), float64(31), 26, json.Number("15")} {
		day, err := normalizeTrafficResetDay(value)
		require.NoError(t, err)
		require.NotNil(t, day)
	}

	for _, value := range []interface{}{float64(-1), float64(32), float64(1.5), "26"} {
		_, err := normalizeTrafficResetDay(value)
		require.Error(t, err)
	}

	day, err := normalizeTrafficResetDay(nil)
	require.NoError(t, err)
	assert.Nil(t, day)
}

func TestSaveClientPersistsCADCurrency(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "komari.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))
	require.NoError(t, db.Create(&models.Client{
		UUID: "client-cad", Token: "token-cad", Name: "Canada Server", Currency: "$",
	}).Error)

	require.NoError(t, saveClient(db, map[string]interface{}{
		"uuid":     "client-cad",
		"currency": " c$ ",
	}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	db, err = gorm.Open(sqlite.Open(databasePath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err = db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	var client models.Client
	require.NoError(t, db.First(&client, "uuid = ?", "client-cad").Error)
	assert.Equal(t, "CAD", client.Currency)
}
