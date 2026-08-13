package notifier

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

func TestCapacityMetricsUseClientTotals(t *testing.T) {
	client := &models.Client{MemTotal: 200, SwapTotal: 400, DiskTotal: 800}
	record := models.Record{
		Ram: 100, RamTotal: 0,
		Swap: 100, SwapTotal: 0,
		Disk: 200, DiskTotal: 0,
	}
	tests := []struct {
		metric string
		want   float32
	}{
		{metric: "ram", want: 50},
		{metric: "swap", want: 25},
		{metric: "disk", want: 25},
	}
	for _, test := range tests {
		if got := getMetricValue(record, test.metric, client); got != test.want {
			t.Fatalf("%s usage = %v, want %v", test.metric, got, test.want)
		}
		if !metricNeedsClientCapacity(test.metric) {
			t.Fatalf("%s should require client capacity", test.metric)
		}
	}
}

func TestCapacityMetricRejectsMissingOrZeroClientTotal(t *testing.T) {
	record := models.Record{Ram: 100}
	if got := getMetricValue(record, "ram", nil); got != 0 {
		t.Fatalf("RAM usage without client = %v, want 0", got)
	}
	if got := getMetricValue(record, "ram", &models.Client{}); got != 0 {
		t.Fatalf("RAM usage with zero capacity = %v, want 0", got)
	}
}

func TestCheckMetricThresholdUsesLoadedCapacity(t *testing.T) {
	records := []models.Record{{Ram: 60}, {Ram: 80}, {Ram: 10}}
	task := models.LoadNotification{Metric: "ram", Threshold: 50, Ratio: 0.5}
	client := &models.Client{MemTotal: 100}
	if !checkMetricThreshold(records, task, client) {
		t.Fatal("two of three RAM samples should exceed the threshold")
	}
	if checkMetricThreshold(records, task, nil) {
		t.Fatal("capacity metric without client data should not trigger")
	}
}

func newLoadNotifierTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:load-notifier-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.LoadNotification{}, &models.LoadNotificationState{}))
	return db
}

func TestPersistLoadClientEvaluationsKeepsSilencedAlertVisibleAndFiltersNotification(t *testing.T) {
	db := newLoadNotifierTestDB(t)
	require.NoError(t, db.Create([]models.Client{
		{UUID: "client-a", Token: "token-a"}, {UUID: "client-b", Token: "token-b"},
	}).Error)
	rule := models.LoadNotification{Name: "Disk", Clients: models.StringArray{"client-a", "client-b"}, Metric: "disk", Interval: 15}
	require.NoError(t, db.Create(&rule).Error)
	now := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
	until := now.Add(24 * time.Hour)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: rule.Id, Client: "client-b", AlertActive: true,
		ActiveSince: &now, LastEvaluatedAt: now, SilencedUntil: &until,
	}).Error)

	notify, current, err := persistLoadClientEvaluationsWithDB(db, rule, []loadClientEvaluation{
		{client: "client-a", active: true, latestValue: 91, matchedSamples: 4, totalSamples: 5},
		{client: "client-b", active: true, latestValue: 93, matchedSamples: 5, totalSamples: 5},
	}, now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, current)
	assert.Equal(t, []string{"client-a"}, notify)

	var states []models.LoadNotificationState
	require.NoError(t, db.Order("client ASC").Find(&states).Error)
	require.Len(t, states, 2)
	assert.True(t, states[0].AlertActive)
	assert.True(t, states[1].AlertActive)
	assert.Equal(t, &until, states[1].SilencedUntil)
}

func TestPersistLoadClientEvaluationsClearsRecoveredAlertAndStartsNewIncident(t *testing.T) {
	db := newLoadNotifierTestDB(t)
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a"}).Error)
	rule := models.LoadNotification{Name: "CPU", Clients: models.StringArray{"client-a"}, Metric: "cpu", Interval: 15}
	require.NoError(t, db.Create(&rule).Error)
	started := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: rule.Id, Client: "client-a", AlertActive: true,
		ActiveSince: &started, LastEvaluatedAt: started,
	}).Error)

	now := started.Add(time.Hour)
	_, current, err := persistLoadClientEvaluationsWithDB(db, rule, []loadClientEvaluation{{client: "client-a"}}, now)
	require.NoError(t, err)
	assert.True(t, current)
	var state models.LoadNotificationState
	require.NoError(t, db.First(&state, "notification_id = ? AND client = ?", rule.Id, "client-a").Error)
	assert.False(t, state.AlertActive)
	assert.Nil(t, state.ActiveSince)

	now = now.Add(time.Minute)
	_, current, err = persistLoadClientEvaluationsWithDB(db, rule, []loadClientEvaluation{{client: "client-a", active: true, totalSamples: 1, matchedSamples: 1}}, now)
	require.NoError(t, err)
	assert.True(t, current)
	require.NoError(t, db.First(&state, "notification_id = ? AND client = ?", rule.Id, "client-a").Error)
	require.NotNil(t, state.ActiveSince)
	assert.Equal(t, now, *state.ActiveSince)
}

func TestPersistLoadClientEvaluationsRejectsStaleRuleSnapshot(t *testing.T) {
	for _, test := range []struct {
		name   string
		update map[string]any
	}{
		{name: "threshold changed", update: map[string]any{"threshold": 90}},
		{name: "name changed", update: map[string]any{"name": "Renamed CPU"}},
		{name: "assignment changed", update: map[string]any{"clients": models.StringArray{"client-b"}}},
		{name: "default-on changed", update: map[string]any{"all_clients": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newLoadNotifierTestDB(t)
			require.NoError(t, db.Create([]models.Client{
				{UUID: "client-a", Token: "token-a"},
				{UUID: "client-b", Token: "token-b"},
			}).Error)
			rule := models.LoadNotification{Name: "CPU", Clients: models.StringArray{"client-a"}, Metric: "cpu", Threshold: 80, Ratio: 0.8, Interval: 15}
			require.NoError(t, db.Create(&rule).Error)
			stale := rule
			require.NoError(t, db.Model(&models.LoadNotification{}).Where("id = ?", rule.Id).Updates(test.update).Error)

			notify, current, err := persistLoadClientEvaluationsWithDB(db, stale, []loadClientEvaluation{
				{client: "client-a", active: true, totalSamples: 1, matchedSamples: 1},
			}, time.Now().UTC())
			require.NoError(t, err)
			assert.False(t, current)
			assert.Empty(t, notify)
			var count int64
			require.NoError(t, db.Model(&models.LoadNotificationState{}).Count(&count).Error)
			assert.Zero(t, count)
		})
	}
}
