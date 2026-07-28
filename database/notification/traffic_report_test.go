package notification

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/komari-monitor/komari/database/models"
)

func TestUpsertTrafficReportNotificationsPersistsContentSelection(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:traffic-report-content?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.TrafficReportNotification{},
	))
	require.NoError(t, db.Create(&models.Client{
		UUID: "client-a", Token: "token-a", Name: "Server A",
	}).Error)

	require.NoError(t, upsertTrafficReportNotifications(db, []models.TrafficReportNotification{{
		Client:         "client-a",
		Enable:         true,
		Daily:          true,
		IncludeTraffic: false,
		IncludeBilling: true,
	}}))

	var created models.TrafficReportNotification
	require.NoError(t, db.First(&created, "client = ?", "client-a").Error)
	assert.False(t, created.IncludeTraffic)
	assert.True(t, created.IncludeBilling)

	require.NoError(t, upsertTrafficReportNotifications(db, []models.TrafficReportNotification{{
		Client:         "client-a",
		Enable:         true,
		Daily:          true,
		IncludeTraffic: true,
		IncludeBilling: false,
	}}))

	var updated models.TrafficReportNotification
	require.NoError(t, db.First(&updated, "client = ?", "client-a").Error)
	assert.True(t, updated.IncludeTraffic)
	assert.False(t, updated.IncludeBilling)
}

func TestValidateTrafficReportNotificationsRejectsEnabledWithoutCadence(t *testing.T) {
	err := ValidateTrafficReportNotifications([]models.TrafficReportNotification{{
		Client:         "client-a",
		Enable:         true,
		IncludeTraffic: true,
	}})

	assert.Error(t, err)
}

func TestValidateTrafficReportNotificationsRejectsEnabledWithoutContent(t *testing.T) {
	err := ValidateTrafficReportNotifications([]models.TrafficReportNotification{{
		Client: "client-a",
		Enable: true,
		Daily:  true,
	}})

	assert.Error(t, err)
}

func TestBuildEnabledTrafficReportNotificationsRequiresExistingCadence(t *testing.T) {
	_, err := buildEnabledTrafficReportNotifications([]string{"client-a"}, nil)
	assert.Error(t, err)

	_, err = buildEnabledTrafficReportNotifications([]string{"client-a"}, []models.TrafficReportNotification{{Client: "client-a"}})
	assert.Error(t, err)

	notifications, err := buildEnabledTrafficReportNotifications([]string{"client-a"}, []models.TrafficReportNotification{{
		Client:         "client-a",
		Daily:          true,
		IncludeTraffic: true,
	}})
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	assert.Equal(t, "client-a", notifications[0].Client)
	assert.True(t, notifications[0].Enable)
}

func TestRequiredTrafficReportRetentionDays(t *testing.T) {
	tests := []struct {
		name          string
		notifications []models.TrafficReportNotification
		want          int
	}{
		{name: "none", want: 0},
		{
			name: "disabled reports do not retain data",
			notifications: []models.TrafficReportNotification{{
				Enable:  false,
				Monthly: true,
			}},
			want: 0,
		},
		{
			name: "daily",
			notifications: []models.TrafficReportNotification{{
				Enable: true,
				Daily:  true,
			}},
			want: 2,
		},
		{
			name: "weekly uses daily ledger safety window",
			notifications: []models.TrafficReportNotification{
				{Enable: true, Daily: true},
				{Enable: true, Weekly: true},
			},
			want: 2,
		},
		{
			name: "monthly uses daily ledger safety window",
			notifications: []models.TrafficReportNotification{{
				Enable:  true,
				Daily:   true,
				Weekly:  true,
				Monthly: true,
			}},
			want: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, RequiredTrafficReportRetentionDays(test.notifications))
		})
	}
}

func TestTrafficReportRetentionRestoresOnlyLegacyAutomaticValuesAfterBackfill(t *testing.T) {
	tests := []struct {
		name         string
		currentDays  int
		requiredDays int
		baselineDays int
		backfilled   bool
		wantDays     int
		wantChanged  bool
	}{
		{name: "reports keep two days", currentDays: 1, requiredDays: 2, baselineDays: 1, wantDays: 2, wantChanged: true},
		{name: "common metric baseline is retained", currentDays: 2, requiredDays: 2, baselineDays: 7, wantDays: 7, wantChanged: true},
		{name: "monthly source remains before backfill", currentDays: 35, requiredDays: 2, baselineDays: 7, wantDays: 35},
		{name: "monthly source restores after backfill", currentDays: 35, requiredDays: 2, baselineDays: 7, backfilled: true, wantDays: 7, wantChanged: true},
		{name: "weekly source restores after backfill", currentDays: 8, requiredDays: 0, baselineDays: 1, backfilled: true, wantDays: 1, wantChanged: true},
		{name: "manual longer retention is preserved", currentDays: 90, requiredDays: 2, baselineDays: 7, backfilled: true, wantDays: 90},
		{name: "disabled metric remains disabled without reports", currentDays: 0, requiredDays: 0, baselineDays: 7, backfilled: true, wantDays: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotDays, gotChanged := trafficReportRetentionTarget(test.currentDays, test.requiredDays, test.baselineDays, test.backfilled)
			assert.Equal(t, test.wantDays, gotDays)
			assert.Equal(t, test.wantChanged, gotChanged)
		})
	}
}
