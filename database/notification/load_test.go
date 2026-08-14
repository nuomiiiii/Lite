package notification

import (
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

func newLoadNotificationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:load-notification-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.LoadNotification{}, &models.LoadNotificationState{}))
	return db
}

func TestAddDefaultOnClientUUIDOnlyUpdatesDefaultRules(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	rules := []models.LoadNotification{
		{Name: "default", Clients: models.StringArray{"client-a"}, DefaultOn: true, Metric: "cpu", Threshold: 80, Ratio: 0.8, Interval: 15},
		{Name: "manual", Clients: models.StringArray{"client-a"}, DefaultOn: false, Metric: "ram", Threshold: 80, Ratio: 0.8, Interval: 15},
		{Name: "already assigned", Clients: models.StringArray{"client-b"}, DefaultOn: true, Metric: "disk", Threshold: 80, Ratio: 0.8, Interval: 15},
	}
	require.NoError(t, db.Create(&rules).Error)

	changed, err := addDefaultOnClientUUID(db, "client-b")
	require.NoError(t, err)
	assert.True(t, changed)

	var got []models.LoadNotification
	require.NoError(t, db.Order("id ASC").Find(&got).Error)
	assert.Equal(t, models.StringArray{"client-a", "client-b"}, got[0].Clients)
	assert.Equal(t, models.StringArray{"client-a"}, got[1].Clients)
	assert.Equal(t, models.StringArray{"client-b"}, got[2].Clients)

	changed, err = addDefaultOnClientUUID(db, "client-b")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestEditLoadNotificationsPersistsDisabledDefaultOn(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	rule := models.LoadNotification{
		Name: "CPU", Clients: models.StringArray{"client-a"}, DefaultOn: true,
		Metric: "cpu", Threshold: 80, Ratio: 0.8, Interval: 15,
	}
	require.NoError(t, db.Create(&rule).Error)

	rule.DefaultOn = false
	require.NoError(t, editLoadNotifications(db, []*models.LoadNotification{&rule}))

	var got models.LoadNotification
	require.NoError(t, db.First(&got, rule.Id).Error)
	assert.False(t, got.DefaultOn)
	assert.Equal(t, models.StringArray{"client-a"}, got.Clients)
}

func TestEditLoadNotificationsRemovesOnlyUnassignedStates(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-a", Token: "token-a", Name: "A"},
		{UUID: "client-b", Token: "token-b", Name: "B"},
	}).Error)
	rule := models.LoadNotification{
		Name: "Disk", Clients: models.StringArray{"client-a", "client-b"},
		Metric: "disk", Threshold: 80, Ratio: 0.8, Interval: 15,
	}
	require.NoError(t, db.Create(&rule).Error)
	require.NoError(t, db.Create([]models.LoadNotificationState{
		{NotificationID: rule.Id, Client: "client-a", RuleFingerprint: models.LoadNotificationRuleFingerprint(rule), AlertActive: true, LastEvaluatedAt: time.Now().UTC()},
		{NotificationID: rule.Id, Client: "client-b", RuleFingerprint: models.LoadNotificationRuleFingerprint(rule), AlertActive: true, LastEvaluatedAt: time.Now().UTC()},
	}).Error)

	rule.Clients = models.StringArray{"client-a"}
	require.NoError(t, editLoadNotifications(db, []*models.LoadNotification{&rule}))

	var states []models.LoadNotificationState
	require.NoError(t, db.Find(&states).Error)
	require.Len(t, states, 1)
	assert.Equal(t, "client-a", states[0].Client)
}

func TestLoadAlertSilenceDurationsAndUnsilence(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	require.NoError(t, db.Create(&models.Client{UUID: "client-b", Token: "token-b", Name: "B"}).Error)
	rule := models.LoadNotification{Name: "Disk", Clients: models.StringArray{"client-b"}, Metric: "disk", Threshold: 80, Ratio: 0.8, Interval: 15}
	require.NoError(t, db.Create(&rule).Error)
	now := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: rule.Id, Client: "client-b", RuleFingerprint: models.LoadNotificationRuleFingerprint(rule), AlertActive: true, LastEvaluatedAt: now,
	}).Error)

	require.NoError(t, setLoadAlertSilence(db, rule.Id, "client-b", "24h", now))
	alerts, err := listCurrentLoadAlerts(db, now)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	assert.True(t, alerts[0].Silenced)
	require.NotNil(t, alerts[0].SilencedUntil)
	assert.Equal(t, now.Add(24*time.Hour), *alerts[0].SilencedUntil)

	after24Hours := now.Add(24 * time.Hour)
	require.NoError(t, db.Model(&models.LoadNotificationState{}).
		Where("notification_id = ? AND client = ?", rule.Id, "client-b").
		Update("last_evaluated_at", after24Hours).Error)
	alerts, err = listCurrentLoadAlerts(db, after24Hours)
	require.NoError(t, err)
	assert.False(t, alerts[0].Silenced)
	assert.Nil(t, alerts[0].SilencedUntil)

	require.NoError(t, setLoadAlertSilence(db, rule.Id, "client-b", "forever", after24Hours))
	afterOneYear := now.AddDate(1, 0, 0)
	require.NoError(t, db.Model(&models.LoadNotificationState{}).
		Where("notification_id = ? AND client = ?", rule.Id, "client-b").
		Update("last_evaluated_at", afterOneYear).Error)
	alerts, err = listCurrentLoadAlerts(db, afterOneYear)
	require.NoError(t, err)
	assert.True(t, alerts[0].Silenced)
	assert.True(t, alerts[0].SilencedForever)

	require.NoError(t, setLoadAlertSilence(db, rule.Id, "client-b", "off", afterOneYear))
	alerts, err = listCurrentLoadAlerts(db, afterOneYear)
	require.NoError(t, err)
	assert.False(t, alerts[0].Silenced)
	assert.False(t, alerts[0].SilencedForever)
}

func TestLoadAlertSilenceRejectsInactiveTarget(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	require.NoError(t, db.Create(&models.Client{UUID: "client-b", Token: "token-b"}).Error)
	rule := models.LoadNotification{Name: "Disk", Clients: models.StringArray{"client-b"}, Metric: "disk", Interval: 15}
	require.NoError(t, db.Create(&rule).Error)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: rule.Id, Client: "client-b", AlertActive: false, LastEvaluatedAt: time.Now().UTC(),
	}).Error)
	assert.Error(t, setLoadAlertSilence(db, rule.Id, "client-b", "24h", time.Now().UTC()))
}

func TestDeleteLoadNotificationDeletesStates(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a"}).Error)
	rule := models.LoadNotification{Name: "CPU", Clients: models.StringArray{"client-a"}, Metric: "cpu", Interval: 15}
	require.NoError(t, db.Create(&rule).Error)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: rule.Id, Client: "client-a", LastEvaluatedAt: time.Now().UTC(),
	}).Error)
	require.NoError(t, deleteLoadNotifications(db, []uint{rule.Id}))
	var stateCount int64
	require.NoError(t, db.Model(&models.LoadNotificationState{}).Count(&stateCount).Error)
	assert.Zero(t, stateCount)
}

func TestEditLoadNotificationSemanticChangeStartsNewIncident(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a"}).Error)
	notified := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	rule := models.LoadNotification{
		Name: "CPU", Clients: models.StringArray{"client-a"}, Metric: "cpu",
		Threshold: 80, Ratio: 0.8, Interval: 15, LastNotified: &notified,
	}
	require.NoError(t, db.Create(&rule).Error)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: rule.Id, Client: "client-a", RuleFingerprint: models.LoadNotificationRuleFingerprint(rule),
		AlertActive: true, LastEvaluatedAt: notified, SilencedForever: true,
	}).Error)

	rule.Threshold = 90
	require.NoError(t, editLoadNotifications(db, []*models.LoadNotification{&rule}))

	var stored models.LoadNotification
	require.NoError(t, db.First(&stored, rule.Id).Error)
	assert.Nil(t, stored.LastNotified)
	var count int64
	require.NoError(t, db.Model(&models.LoadNotificationState{}).Where("notification_id = ?", rule.Id).Count(&count).Error)
	assert.Zero(t, count)
}

func TestListCurrentLoadAlertsExcludesStaleAndMismatchedState(t *testing.T) {
	db := newLoadNotificationTestDB(t)
	require.NoError(t, db.Create([]models.Client{
		{UUID: "fresh", Token: "token-fresh"},
		{UUID: "stale", Token: "token-stale"},
		{UUID: "changed", Token: "token-changed"},
	}).Error)
	rule := models.LoadNotification{
		Name: "CPU", Clients: models.StringArray{"fresh", "stale", "changed"},
		Metric: "cpu", Threshold: 80, Ratio: 0.8, Interval: 5,
	}
	require.NoError(t, db.Create(&rule).Error)
	now := time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC)
	fingerprint := models.LoadNotificationRuleFingerprint(rule)
	require.NoError(t, db.Create([]models.LoadNotificationState{
		{NotificationID: rule.Id, Client: "fresh", RuleFingerprint: fingerprint, AlertActive: true, LastEvaluatedAt: now.Add(-time.Minute)},
		{NotificationID: rule.Id, Client: "stale", RuleFingerprint: fingerprint, AlertActive: true, LastEvaluatedAt: now.Add(-10 * time.Minute)},
		{NotificationID: rule.Id, Client: "changed", RuleFingerprint: "old-rule", AlertActive: true, LastEvaluatedAt: now.Add(-time.Minute)},
	}).Error)

	alerts, err := listCurrentLoadAlerts(db, now)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	assert.Equal(t, "fresh", alerts[0].Client)
}
