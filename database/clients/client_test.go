package clients

import (
	"encoding/json"
	"fmt"
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
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}))

	createdAt := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-c", Token: "token-c", Name: "C", Weight: 20, CreatedAt: createdAt},
		{UUID: "client-b", Token: "token-b", Name: "B", Weight: 10, CreatedAt: createdAt.Add(time.Minute)},
		{UUID: "client-a", Token: "token-a", Name: "A", Weight: 10, CreatedAt: createdAt},
	}).Error)
	require.NoError(t, db.Create([]models.ClientDeploymentProfile{
		{Client: "client-a", Config: `{}`, Revision: 1, DeliveryStatus: DeploymentDeliveryApplied},
		{Client: "client-c", Config: `{}`, Revision: 2, DeliveryStatus: DeploymentDeliveryFailed},
	}).Error)

	clients, err := getClientBasicInfo(db)
	require.NoError(t, err)
	require.Len(t, clients, 3)
	assert.Equal(t, []string{"client-a", "client-b", "client-c"}, []string{
		clients[0].UUID, clients[1].UUID, clients[2].UUID,
	})
	assert.Equal(t, DeploymentDeliveryApplied, clients[0].DeploymentStatus)
	assert.Empty(t, clients[1].DeploymentStatus)
	assert.Equal(t, DeploymentDeliveryFailed, clients[2].DeploymentStatus)
}

func TestNewClientDefaultsTrafficLimitTypeToSum(t *testing.T) {
	now := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	client := newClient("client-new", "token-new", "New Server", now)

	assert.Equal(t, "sum", client.TrafficLimitType)
	assert.Equal(t, now, client.CreatedAt)
	assert.Equal(t, now, client.UpdatedAt)
}

func TestSaveClientKeepsExistingTrafficLimitTypeWhenOmitted(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:keep-existing-traffic-type?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))
	require.NoError(t, db.Create(&models.Client{
		UUID: "client-existing", Token: "token-existing", Name: "Existing Server", TrafficLimitType: "max",
	}).Error)

	require.NoError(t, saveClient(db, map[string]interface{}{
		"uuid": "client-existing",
		"name": "Renamed Server",
	}))

	var client models.Client
	require.NoError(t, db.First(&client, "uuid = ?", "client-existing").Error)
	assert.Equal(t, "max", client.TrafficLimitType)
	assert.Equal(t, "Renamed Server", client.Name)
}

func TestDeleteClientCleansAllRelatedRowsAndSharedAssignments(t *testing.T) {
	for _, foreignKeys := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign_keys_%t", foreignKeys), func(t *testing.T) {
			dsn := fmt.Sprintf("file:delete-client-cleanup-%t?mode=memory&cache=shared&_foreign_keys=%t", foreignKeys, foreignKeys)
			db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
			}
			require.NoError(t, db.Create([]models.Client{
				{UUID: "client-a", Token: "token-a", Name: "Server A"},
				{UUID: "client-b", Token: "token-b", Name: "Server B"},
			}).Error)

			pingTask := models.PingTask{
				Name: "Public DNS", Clients: models.StringArray{"client-a", "client-b"},
				Type: "icmp", Target: "1.1.1.1", Interval: 10,
			}
			require.NoError(t, db.Create(&pingTask).Error)
			require.NoError(t, db.Create([]models.PingLossNotification{
				{Client: "client-a", TaskId: pingTask.Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
				{Client: "client-b", TaskId: pingTask.Id, Enable: true, WindowSeconds: 60, LossThreshold: 5, MinimumSamples: 1, CooldownSeconds: 300},
			}).Error)
			require.NoError(t, db.Create([]models.OfflineNotification{{Client: "client-a"}, {Client: "client-b"}}).Error)
			require.NoError(t, db.Create([]models.TrafficReportNotification{{Client: "client-a"}, {Client: "client-b"}}).Error)
			require.NoError(t, db.Create([]models.TrafficDailyLedger{
				{Client: "client-a", Day: "2026-07-24", UpBytes: 100, DownBytes: 200},
				{Client: "client-b", Day: "2026-07-24", UpBytes: 300, DownBytes: 400},
			}).Error)
			loadRules := []models.LoadNotification{
				{Name: "shared", Clients: models.StringArray{"client-a", "client-b"}, Metric: "cpu", Interval: 15},
				{Name: "only-a", Clients: models.StringArray{"client-a"}, Metric: "cpu", Interval: 15},
			}
			require.NoError(t, db.Create(&loadRules).Error)
			require.NoError(t, db.Create([]models.LoadNotificationState{
				{NotificationID: loadRules[0].Id, Client: "client-a", AlertActive: true},
				{NotificationID: loadRules[0].Id, Client: "client-b", AlertActive: true},
				{NotificationID: loadRules[1].Id, Client: "client-a", AlertActive: true},
			}).Error)
			commandTasks := []models.Task{
				{TaskId: "shared", Clients: models.StringArray{"client-a", "client-b"}, Command: "uptime"},
				{TaskId: "only-a", Clients: models.StringArray{"client-a"}, Command: "uptime"},
			}
			require.NoError(t, db.Create(&commandTasks).Error)
			require.NoError(t, db.Create([]models.TaskResult{
				{TaskId: "shared", Client: "client-a"},
				{TaskId: "shared", Client: "client-b"},
				{TaskId: "only-a", Client: "client-a"},
			}).Error)
			for _, table := range []string{"records", "records_long_term", "gpu_records", "ping_records"} {
				require.NoError(t, db.Exec("INSERT INTO "+table+" (client, task_id) VALUES (?, ?), (?, ?)",
					"client-a", pingTask.Id, "client-b", pingTask.Id).Error)
			}

			changed, err := deleteClient(db, "client-a")
			require.NoError(t, err)
			assert.True(t, changed)

			assertRowCount(t, db, &models.Client{}, "uuid = ?", 0, "client-a")
			assertRowCount(t, db, &models.PingLossNotification{}, "client = ?", 0, "client-a")
			assertRowCount(t, db, &models.OfflineNotification{}, "client = ?", 0, "client-a")
			assertRowCount(t, db, &models.TrafficReportNotification{}, "client = ?", 0, "client-a")
			assertRowCount(t, db, &models.TrafficDailyLedger{}, "client = ?", 0, "client-a")
			assertRowCount(t, db, &models.LoadNotificationState{}, "client = ?", 0, "client-a")
			assertRowCount(t, db, &models.TaskResult{}, "client = ?", 0, "client-a")
			for _, table := range []string{"records", "records_long_term", "gpu_records", "ping_records"} {
				var count int64
				require.NoError(t, db.Table(table).Where("client = ?", "client-a").Count(&count).Error)
				assert.Zero(t, count, table)
			}

			var gotPingTask models.PingTask
			require.NoError(t, db.First(&gotPingTask, pingTask.Id).Error)
			assert.Equal(t, models.StringArray{"client-b"}, gotPingTask.Clients)
			var remainingLoadRules []models.LoadNotification
			require.NoError(t, db.Order("id ASC").Find(&remainingLoadRules).Error)
			require.Len(t, remainingLoadRules, 1)
			assert.Equal(t, models.StringArray{"client-b"}, remainingLoadRules[0].Clients)
			assertRowCount(t, db, &models.LoadNotificationState{}, "notification_id = ? AND client = ?", 1, loadRules[0].Id, "client-b")
			var remainingTasks []models.Task
			require.NoError(t, db.Order("task_id ASC").Find(&remainingTasks).Error)
			require.Len(t, remainingTasks, 1)
			assert.Equal(t, "shared", remainingTasks[0].TaskId)
			assert.Equal(t, models.StringArray{"client-b"}, remainingTasks[0].Clients)
			assertRowCount(t, db, &models.TaskResult{}, "client = ?", 1, "client-b")
		})
	}
}

func assertRowCount(t *testing.T, db *gorm.DB, model any, query string, expected int64, args ...any) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(model).Where(query, args...).Count(&count).Error)
	assert.Equal(t, expected, count)
}

func TestDeleteClientRollsBackRelatedRowsWhenClientDeleteFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:delete-client-rollback?mode=memory&cache=shared&_foreign_keys=off"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{}, &models.PingTask{}, &models.PingLossNotification{},
		&models.OfflineNotification{}, &models.TrafficReportNotification{},
		&models.LoadNotification{}, &models.Task{}, &models.TaskResult{},
	))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a"}).Error)
	require.NoError(t, db.Create(&models.OfflineNotification{Client: "client-a", Enable: true}).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_client_delete BEFORE DELETE ON clients
		BEGIN SELECT RAISE(FAIL, 'delete rejected'); END`).Error)

	_, err = deleteClient(db, "client-a")
	require.Error(t, err)
	assertRowCount(t, db, &models.Client{}, "uuid = ?", 1, "client-a")
	assertRowCount(t, db, &models.OfflineNotification{}, "client = ?", 1, "client-a")
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

func TestRotateClientTokenKeepsOldTokenUntilNewTokenConnects(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:client-token-rotation?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "old-token", Name: "A"}).Error)

	newToken, expiresAt, err := rotateClientToken(db, "client-a", time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, newToken)
	require.NotEqual(t, "old-token", newToken)

	uuid, err := getClientUUIDByToken(db, "old-token", expiresAt.Add(-time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "client-a", uuid)
	_, _, err = rotateClientToken(db, "client-a", time.Hour)
	require.Error(t, err)
	assert.Equal(t, "Token 重置仍在过渡期内，请先使用新 Token 重新部署 Agent；新 Token 首次成功连接后才能再次重置", err.Error())

	uuid, err = getClientUUIDByToken(db, newToken, expiresAt.Add(-time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "client-a", uuid)

	_, err = getClientUUIDByToken(db, "old-token", expiresAt.Add(-time.Minute))
	require.Error(t, err)

	var client models.Client
	require.NoError(t, db.First(&client, "uuid = ?", "client-a").Error)
	assert.Empty(t, client.PreviousToken)
	assert.Nil(t, client.PreviousTokenExpiresAt)
}

func TestExpiredPreviousClientTokenIsRejected(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:client-token-expiry?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))
	expiresAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Create(&models.Client{
		UUID: "client-a", Token: "new-token", PreviousToken: "old-token", PreviousTokenExpiresAt: &expiresAt,
	}).Error)

	_, err = getClientUUIDByToken(db, "old-token", time.Now().UTC())
	require.Error(t, err)
}
