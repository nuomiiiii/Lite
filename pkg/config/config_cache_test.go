package config

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openConfigTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", name)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	SetDb(gdb)
	return gdb
}

type sampleConfigView struct {
	Sitename string `json:"sitename" default:"Lite"`
	MaxRetry int    `json:"max_retry" default:"3"`
	Optional string `json:"optional"`
}

func TestConfigFullTableCacheGenerationAndZeroQueryReads(t *testing.T) {
	gdb := openConfigTestDB(t, "config_cache_gen_behavior")

	var queryCount atomic.Int64
	const cbName = "test:config_query_counter"
	_ = gdb.Callback().Query().After("gorm:query").Register(cbName, func(*gorm.DB) {
		queryCount.Add(1)
	})
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove(cbName) })

	if err := Set("sitename", "InitialSite"); err != nil {
		t.Fatalf("Set sitename: %v", err)
	}
	genAfterSet := CacheGeneration()

	// Warm cache if needed, then verify repeated reads make ZERO SQLite SELECTs.
	got, err := GetAs[string]("sitename")
	if err != nil || got != "InitialSite" {
		t.Fatalf("GetAs sitename = %q, err=%v", got, err)
	}
	queryCount.Store(0)
	for range 5 {
		v, err := GetAs[string]("sitename")
		if err != nil || v != "InitialSite" {
			t.Fatalf("cached GetAs = %q, err=%v", v, err)
		}
		many, err := GetMany(map[string]any{"sitename": "Fallback", "missing_nil": nil})
		if err != nil || many["sitename"] != "InitialSite" {
			t.Fatalf("cached GetMany = %#v, err=%v", many, err)
		}
		all, err := GetAll()
		if err != nil || all["sitename"] != "InitialSite" {
			t.Fatalf("cached GetAll = %#v, err=%v", all, err)
		}
	}
	if q := queryCount.Load(); q != 0 {
		t.Fatalf("expected 0 SQLite SELECT queries on warm config cache, got %d", q)
	}

	// Stale generation default backfill must NOT overwrite concurrent Set or invalidated cache.
	staleGen := CacheGeneration()
	if err := Set("max_retry", 9); err != nil {
		t.Fatalf("Set max_retry: %v", err)
	}
	if CacheGeneration() <= genAfterSet {
		t.Fatalf("expected CacheGeneration to advance after Set")
	}
	putCachedDefaultsIfGen(gdb, staleGen, []ConfigItem{{Key: "max_retry", Value: "3"}, {Key: "ghost", Value: `"stale"`}})
	retryVal, err := GetAs[int]("max_retry")
	if err != nil || retryVal != 9 {
		t.Fatalf("stale default backfill overwrote max_retry: got %d err=%v", retryVal, err)
	}
	if _, err := GetAs[string]("ghost"); err == nil {
		t.Fatal("stale default backfill inserted ghost key despite mismatched generation")
	}

	// Direct SQL mutation + InvalidateCache must bump generation and reload from SQLite.
	beforeInvGen := CacheGeneration()
	if err := gdb.Model(&ConfigItem{}).Where("key = ?", "sitename").Update("value", `"FromRawSQL"`).Error; err != nil {
		t.Fatalf("raw SQL update: %v", err)
	}
	InvalidateCache()
	if CacheGeneration() <= beforeInvGen {
		t.Fatal("expected CacheGeneration to advance on InvalidateCache")
	}
	reloaded, err := GetManyAs[sampleConfigView]()
	if err != nil {
		t.Fatalf("GetManyAs after InvalidateCache: %v", err)
	}
	if reloaded.Sitename != "FromRawSQL" || reloaded.MaxRetry != 9 || reloaded.Optional != "" {
		t.Fatalf("unexpected GetManyAs after reload: %+v", reloaded)
	}
}

func TestConfigCacheConcurrentReadWriteInvalidate(t *testing.T) {
	openConfigTestDB(t, "config_cache_concurrent")

	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range 40 {
				key := fmt.Sprintf("k_%d", i%6)
				_ = Set(key, worker*100+i)
				if i%7 == 0 {
					_ = SetMany(map[string]any{
						"batch_a": i,
						"batch_b": fmt.Sprintf("v-%d", i),
					})
				}
				if i%11 == 0 {
					InvalidateCache()
				}
			}
		}(w)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				_, _ = GetAs[int]("k_0", 1)
				_, _ = GetMany(map[string]any{"k_1": 2, "k_2": nil})
				_, _ = GetManyAs[sampleConfigView]()
				if i%5 == 0 {
					_, _ = GetAll()
				}
			}
		}()
	}
	wg.Wait()

	if err := Set("final_check", "ok"); err != nil {
		t.Fatalf("final Set: %v", err)
	}
	got, err := GetAs[string]("final_check")
	if err != nil || got != "ok" {
		t.Fatalf("final GetAs = %q, err=%v", got, err)
	}
}
