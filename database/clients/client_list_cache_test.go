package clients

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestClientBasicInfoCacheReusesThenDrops(t *testing.T) {
	var cache clientBasicInfoCache
	db := &gorm.DB{}
	now := time.Date(2026, 10, 6, 21, 0, 0, 0, time.UTC)
	day := 6
	gen := cache.generation(db)
	cache.set(db, gen, now, []models.Client{{UUID: "a", Name: "alpha", TrafficResetDay: &day}})

	got, ok := cache.get(db, now.Add(19*time.Second), clientListCacheTTL)
	if !ok || len(got) != 1 || got[0].Name != "alpha" || got[0].TrafficResetDay == nil || *got[0].TrafficResetDay != 6 {
		t.Fatalf("fresh cache = %#v ok=%v", got, ok)
	}
	got[0].Name = "changed"
	*got[0].TrafficResetDay = 1

	again, ok := cache.get(db, now.Add(19*time.Second), clientListCacheTTL)
	if !ok || again[0].Name != "alpha" || *again[0].TrafficResetDay != 6 {
		t.Fatalf("cache shared caller memory: %#v", again)
	}
	if _, ok := cache.get(db, now.Add(clientListCacheTTL), clientListCacheTTL); ok {
		t.Fatal("cache lived past its window")
	}
	staleGen := cache.generation(db)
	cache.invalidate()
	cache.set(db, staleGen, now, []models.Client{{UUID: "a", Name: "stale"}})
	if _, ok := cache.get(db, now, clientListCacheTTL); ok {
		t.Fatal("stale generation write repopulated invalidated cache")
	}
	gen = cache.generation(db)
	cache.set(db, gen, now, []models.Client{{UUID: "a", Name: "alpha"}})
	cache.invalidate()
	if _, ok := cache.get(db, now, clientListCacheTTL); ok {
		t.Fatal("invalidated cache still hit")
	}
}

func TestGetClientBasicInfoDoesNotWriteExpiredAllowance(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:client-list-no-persist?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}))
	day := 1
	require.NoError(t, db.Create(&models.Client{
		UUID: "node-a", Token: "token-a", TrafficLimit: 100, TrafficLimitType: "max",
		TrafficResetDay: &day, TrafficResetAllowance: 50, TrafficResetCycle: "2000-01-01",
	}).Error)

	got, err := getClientBasicInfo(db)
	require.NoError(t, err)
	require.Len(t, got, 1)
	if got[0].TrafficResetAllowance != 0 || got[0].EffectiveTrafficLimit != 100 {
		t.Fatalf("displayed allowance = %d limit %d", got[0].TrafficResetAllowance, got[0].EffectiveTrafficLimit)
	}

	var stored models.Client
	require.NoError(t, db.First(&stored, "uuid = ?", "node-a").Error)
	if stored.TrafficResetAllowance != 50 || stored.TrafficResetCycle != "2000-01-01" {
		t.Fatalf("list read wrote the row: allowance %d cycle %q", stored.TrafficResetAllowance, stored.TrafficResetCycle)
	}

	require.NoError(t, applyClientDisplayFieldsAndPersist(db, []models.Client{stored}, time.Now().UTC()))
	require.NoError(t, db.First(&stored, "uuid = ?", "node-a").Error)
	if stored.TrafficResetAllowance != 0 || stored.TrafficResetCycle != "" {
		t.Fatalf("persist left allowance %d cycle %q", stored.TrafficResetAllowance, stored.TrafficResetCycle)
	}
}
