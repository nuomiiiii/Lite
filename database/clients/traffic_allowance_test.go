package clients

import (
	"math"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func trafficDay(day int) *int { return &day }

type legacyTrafficClient struct {
	UUID             string `gorm:"type:varchar(36);primaryKey"`
	Token            string `gorm:"type:varchar(64);not null"`
	Name             string
	Region           string
	TrafficLimit     int64
	TrafficLimitType string
	TrafficResetDay  *int
}

func (legacyTrafficClient) TableName() string { return "clients" }

func TestCurrentTrafficCycleUsesMostRecentBeijingResetDay(t *testing.T) {
	assert.Equal(t, "2026-07-26", currentTrafficCycle(trafficDay(26), time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)))
	assert.Equal(t, "2026-08-01", currentTrafficCycle(trafficDay(1), time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)))
	assert.Equal(t, "2026-02-28", currentTrafficCycle(trafficDay(31), time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)))
	assert.Empty(t, currentTrafficCycle(trafficDay(0), time.Now()))
}

func TestApplyClientDisplayFieldsAddsCurrentCycleResetTraffic(t *testing.T) {
	client := models.Client{
		Region: "🇺🇸", RegionOverride: "🇸🇬",
		TrafficLimit: 100, TrafficLimitType: "max", TrafficResetDay: trafficDay(26),
		TrafficResetAllowance: 50, TrafficResetCycle: "2026-07-26",
	}
	changed := applyClientDisplayFields(&client, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	assert.False(t, changed)
	assert.Equal(t, "🇸🇬", client.Region)
	assert.Equal(t, int64(150), client.EffectiveTrafficLimit)
	assert.Equal(t, "max", client.EffectiveTrafficType)
}

func TestApplyClientDisplayFieldsExpiresPreviousCycleResetTraffic(t *testing.T) {
	client := models.Client{
		TrafficLimit: 100, TrafficLimitType: "max", TrafficResetDay: trafficDay(1),
		TrafficResetAllowance: 50, TrafficResetCycle: "2026-07-01",
	}
	changed := applyClientDisplayFields(&client, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	assert.True(t, changed)
	assert.Zero(t, client.TrafficResetAllowance)
	assert.Empty(t, client.TrafficResetCycle)
	assert.Equal(t, int64(100), client.EffectiveTrafficLimit)
	assert.Equal(t, "max", client.EffectiveTrafficType)
}

func TestSaveClientNormalizesRegionAndBindsResetTrafficToCycle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:traffic-allowance-save?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))
	day := 26
	require.NoError(t, db.Create(&models.Client{UUID: "node-a", Token: "token-a", TrafficLimit: 100, TrafficResetDay: &day}).Error)

	require.NoError(t, saveClient(db, map[string]interface{}{
		"uuid": "node-a", "region_override": "sg", "traffic_reset_allowance": float64(50),
	}))
	var client models.Client
	require.NoError(t, db.First(&client, "uuid = ?", "node-a").Error)
	assert.Equal(t, "🇸🇬", client.RegionOverride)
	assert.Equal(t, int64(50), client.TrafficResetAllowance)
	assert.NotEmpty(t, client.TrafficResetCycle)
}

func TestSaveClientRebindsAllowanceWhenResetDayChanges(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:traffic-allowance-rebind?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))
	oldDay := 26
	require.NoError(t, db.Create(&models.Client{
		UUID: "node-a", Token: "token-a", TrafficLimit: 100,
		TrafficResetDay: &oldDay, TrafficResetAllowance: 50, TrafficResetCycle: "2026-07-26",
	}).Error)

	require.NoError(t, saveClient(db, map[string]interface{}{
		"uuid": "node-a", "traffic_reset_day": float64(1),
	}))
	var client models.Client
	require.NoError(t, db.First(&client, "uuid = ?", "node-a").Error)
	assert.Equal(t, int64(50), client.TrafficResetAllowance)
	assert.Equal(t, currentTrafficCycle(trafficDay(1), time.Now().UTC()), client.TrafficResetCycle)
}

func TestSaveClientClearsAllowanceWhenResetDayIsDisabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:traffic-allowance-disable?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}))
	day := 26
	require.NoError(t, db.Create(&models.Client{
		UUID: "node-a", Token: "token-a", TrafficLimit: 100,
		TrafficResetDay: &day, TrafficResetAllowance: 50, TrafficResetCycle: "2026-07-26",
	}).Error)

	require.NoError(t, saveClient(db, map[string]interface{}{
		"uuid": "node-a", "traffic_reset_day": float64(0),
	}))
	var client models.Client
	require.NoError(t, db.First(&client, "uuid = ?", "node-a").Error)
	assert.Zero(t, client.TrafficResetAllowance)
	assert.Empty(t, client.TrafficResetCycle)
}

func TestToInt64RejectsFloatOutsideInt64Range(t *testing.T) {
	_, ok := toInt64(math.Exp2(63))
	assert.False(t, ok)
	_, ok = toInt64(math.Inf(1))
	assert.False(t, ok)
}

func TestLegacyClientTableMigratesResetAllowanceAndRegionOverride(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:traffic-allowance-legacy?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyTrafficClient{}))
	day := 15
	require.NoError(t, db.Create(&legacyTrafficClient{
		UUID: "legacy-node", Token: "legacy-token", Name: "Legacy", Region: "US",
		TrafficLimit: 1024, TrafficLimitType: "max", TrafficResetDay: &day,
	}).Error)

	require.NoError(t, db.AutoMigrate(&models.Client{}))
	var migrated models.Client
	require.NoError(t, db.First(&migrated, "uuid = ?", "legacy-node").Error)
	assert.Equal(t, "Legacy", migrated.Name)
	assert.Equal(t, int64(1024), migrated.TrafficLimit)
	assert.Empty(t, migrated.RegionOverride)
	assert.Zero(t, migrated.TrafficResetAllowance)
	assert.Empty(t, migrated.TrafficResetCycle)
}
