package notificationdefaults

import (
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupNotificationDefaultsTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.PingTask{},
		&models.OfflineNotification{},
		&models.PingLossNotification{},
		&models.TrafficReportNotification{},
	))
	config.SetDb(db)
	return db
}

func TestNotificationDefaultsPersistAndValidate(t *testing.T) {
	setupNotificationDefaultsTestDB(t, "notification-default-config")

	offline, err := GetOfflineNotificationDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, defaultOfflineNotificationConfig, offline)
	pingLoss, err := GetPingLossNotificationDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, defaultPingLossNotificationConfig, pingLoss)
	trafficReport, err := GetTrafficReportDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, defaultTrafficReportConfig, trafficReport)

	wantOffline := OfflineNotificationDefaultConfig{Enabled: true, GracePeriod: 420}
	require.NoError(t, SetOfflineNotificationDefaultConfig(wantOffline))
	gotOffline, err := GetOfflineNotificationDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, wantOffline, gotOffline)
	require.Error(t, SetOfflineNotificationDefaultConfig(OfflineNotificationDefaultConfig{
		Enabled: true,
	}))

	wantPingLoss := PingLossNotificationDefaultConfig{
		Enabled:         true,
		WindowSeconds:   180,
		LossThreshold:   12.5,
		MinimumSamples:  7,
		CooldownSeconds: 900,
	}
	require.NoError(t, SetPingLossNotificationDefaultConfig(wantPingLoss))
	gotPingLoss, err := GetPingLossNotificationDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, wantPingLoss, gotPingLoss)
	invalidPingLoss := wantPingLoss
	invalidPingLoss.WindowSeconds = 30
	require.Error(t, SetPingLossNotificationDefaultConfig(invalidPingLoss))

	gotPingLoss, err = GetPingLossNotificationDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, wantPingLoss, gotPingLoss)

	wantTrafficReport := TrafficReportDefaultConfig{
		Enabled: true, Daily: false, Weekly: true, Monthly: true,
		IncludeTraffic: true, IncludeBilling: true,
	}
	require.NoError(t, SetTrafficReportDefaultConfig(wantTrafficReport))
	gotTrafficReport, err := GetTrafficReportDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, wantTrafficReport, gotTrafficReport)
	invalidTrafficReport := wantTrafficReport
	invalidTrafficReport.Daily = false
	invalidTrafficReport.Weekly = false
	invalidTrafficReport.Monthly = false
	require.Error(t, SetTrafficReportDefaultConfig(invalidTrafficReport))
	invalidTrafficReport = wantTrafficReport
	invalidTrafficReport.IncludeTraffic = false
	invalidTrafficReport.IncludeBilling = false
	require.Error(t, SetTrafficReportDefaultConfig(invalidTrafficReport))
	gotTrafficReport, err = GetTrafficReportDefaultConfig()
	require.NoError(t, err)
	assert.Equal(t, wantTrafficReport, gotTrafficReport)
}

func TestApplyDefaultsToNewClientTargetsAssignedTasksOnly(t *testing.T) {
	db := setupNotificationDefaultsTestDB(t, "notification-default-apply")
	require.NoError(t, db.Create(&[]models.Client{
		{UUID: "client-a", Token: "token-a", Name: "Server A"},
		{UUID: "client-b", Token: "token-b", Name: "Server B"},
	}).Error)
	assignedTask := models.PingTask{
		Name: "Assigned", Clients: models.StringArray{"client-a"}, Type: "icmp", Target: "1.1.1.1", Interval: 10,
	}
	unassignedTask := models.PingTask{
		Name: "Unassigned", Clients: models.StringArray{"client-b"}, Type: "icmp", Target: "8.8.8.8", Interval: 10,
	}
	require.NoError(t, db.Create(&assignedTask).Error)
	require.NoError(t, db.Create(&unassignedTask).Error)
	require.NoError(t, SetOfflineNotificationDefaultConfig(OfflineNotificationDefaultConfig{
		Enabled: true, GracePeriod: 420,
	}))
	require.NoError(t, SetPingLossNotificationDefaultConfig(PingLossNotificationDefaultConfig{
		Enabled: true, WindowSeconds: 180, LossThreshold: 12.5, MinimumSamples: 7, CooldownSeconds: 900,
	}))
	require.NoError(t, SetTrafficReportDefaultConfig(TrafficReportDefaultConfig{
		Enabled: true, Daily: false, Weekly: true, Monthly: true,
		IncludeTraffic: true, IncludeBilling: true,
	}))

	trafficReportApplied, err := applyDefaultsToNewClient(db, "client-a")
	require.NoError(t, err)
	assert.True(t, trafficReportApplied)
	var offline models.OfflineNotification
	require.NoError(t, db.Where("client = ?", "client-a").First(&offline).Error)
	assert.True(t, offline.Enable)
	assert.Equal(t, 420, offline.GracePeriod)

	var rules []models.PingLossNotification
	require.NoError(t, db.Where("client = ?", "client-a").Find(&rules).Error)
	require.Len(t, rules, 1)
	assert.Equal(t, assignedTask.Id, rules[0].TaskId)
	assert.True(t, rules[0].Enable)
	assert.Equal(t, 180, rules[0].WindowSeconds)
	assert.Equal(t, 12.5, rules[0].LossThreshold)
	assert.Equal(t, 7, rules[0].MinimumSamples)
	assert.Equal(t, 900, rules[0].CooldownSeconds)
	var trafficReport models.TrafficReportNotification
	require.NoError(t, db.Where("client = ?", "client-a").First(&trafficReport).Error)
	assert.True(t, trafficReport.Enable)
	assert.False(t, trafficReport.Daily)
	assert.True(t, trafficReport.Weekly)
	assert.True(t, trafficReport.Monthly)
	assert.True(t, trafficReport.IncludeTraffic)
	assert.True(t, trafficReport.IncludeBilling)

	require.NoError(t, db.Model(&models.OfflineNotification{}).
		Where("client = ?", "client-a").Update("grace_period", 999).Error)
	require.NoError(t, db.Model(&models.PingLossNotification{}).
		Where("client = ? AND task_id = ?", "client-a", assignedTask.Id).
		Update("loss_threshold", 33).Error)
	require.NoError(t, db.Model(&models.TrafficReportNotification{}).
		Where("client = ?", "client-a").Update("daily", true).Error)
	trafficReportApplied, err = applyDefaultsToNewClient(db, "client-a")
	require.NoError(t, err)
	assert.False(t, trafficReportApplied)
	require.NoError(t, db.Where("client = ?", "client-a").First(&offline).Error)
	assert.Equal(t, 999, offline.GracePeriod)
	require.NoError(t, db.Where("client = ?", "client-a").Find(&rules).Error)
	require.Len(t, rules, 1)
	assert.Equal(t, 33.0, rules[0].LossThreshold)
	require.NoError(t, db.Where("client = ?", "client-a").First(&trafficReport).Error)
	assert.True(t, trafficReport.Daily)

	require.NoError(t, SetOfflineNotificationDefaultConfig(OfflineNotificationDefaultConfig{
		Enabled: false, GracePeriod: 420,
	}))
	require.NoError(t, SetPingLossNotificationDefaultConfig(PingLossNotificationDefaultConfig{
		Enabled: false, WindowSeconds: 180, LossThreshold: 12.5, MinimumSamples: 7, CooldownSeconds: 900,
	}))
	require.NoError(t, SetTrafficReportDefaultConfig(TrafficReportDefaultConfig{
		Enabled: false, Daily: true, IncludeTraffic: true,
	}))
	trafficReportApplied, err = applyDefaultsToNewClient(db, "client-b")
	require.NoError(t, err)
	assert.False(t, trafficReportApplied)
	var count int64
	require.NoError(t, db.Model(&models.OfflineNotification{}).Where("client = ?", "client-b").Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Model(&models.PingLossNotification{}).Where("client = ?", "client-b").Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Model(&models.TrafficReportNotification{}).Where("client = ?", "client-b").Count(&count).Error)
	assert.Zero(t, count)
}

func TestApplyPingLossDefaultsToTaskClientsCreatesMissingRulesOnly(t *testing.T) {
	db := setupNotificationDefaultsTestDB(t, "notification-default-ping-task")
	task := models.PingTask{
		Name: "test", Clients: models.StringArray{"client-a", "client-b"}, Type: "icmp", Target: "1.1.1.1", Interval: 10,
	}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&models.PingLossNotification{
		Client: "client-a", TaskId: task.Id, Enable: true, WindowSeconds: 120, LossThreshold: 5, MinimumSamples: 2, CooldownSeconds: 180,
	}).Error)
	require.NoError(t, SetPingLossNotificationDefaultConfig(PingLossNotificationDefaultConfig{
		Enabled: true, WindowSeconds: 60, LossThreshold: 30, MinimumSamples: 6, CooldownSeconds: 60,
	}))

	require.NoError(t, ApplyPingLossDefaultsToTaskClients(db, task.Id, []string{"client-a", "client-b"}))

	var rules []models.PingLossNotification
	require.NoError(t, db.Order("client ASC").Where("task_id = ?", task.Id).Find(&rules).Error)
	require.Len(t, rules, 2)
	assert.Equal(t, "client-a", rules[0].Client)
	assert.Equal(t, 120, rules[0].WindowSeconds)
	assert.Equal(t, 5.0, rules[0].LossThreshold)
	assert.Equal(t, "client-b", rules[1].Client)
	assert.True(t, rules[1].Enable)
	assert.Equal(t, 60, rules[1].WindowSeconds)
	assert.Equal(t, 30.0, rules[1].LossThreshold)
	assert.Equal(t, 6, rules[1].MinimumSamples)
	assert.Equal(t, 60, rules[1].CooldownSeconds)

	require.NoError(t, SetPingLossNotificationDefaultConfig(PingLossNotificationDefaultConfig{
		Enabled: false, WindowSeconds: 60, LossThreshold: 30, MinimumSamples: 6, CooldownSeconds: 60,
	}))
	require.NoError(t, ApplyPingLossDefaultsToTaskClients(db, task.Id, []string{"client-c"}))
	require.NoError(t, db.Where("task_id = ?", task.Id).Find(&rules).Error)
	require.Len(t, rules, 2)
}
