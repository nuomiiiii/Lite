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
	require.NoError(t, db.AutoMigrate(&models.LoadNotification{}))
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
