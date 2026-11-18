package metric

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// TestBackendBuildersApplyOptions verifies backend builders apply options.
//
// TestBackendBuildersApplyOptions 验证各后端构造器会正确应用选项。
func TestBackendBuildersApplyOptions(t *testing.T) {
	cfg := SQLite(
		"file:metrics.db?cache=shared",
		WithTablePrefix("x_metric_"),
		WithAutoMigrate(false),
		WithMaxOpenConns(10),
		WithMaxIdleConns(2),
		WithConnMaxLifetime(30*time.Minute),
		WithConnectTimeout(2*time.Second),
	)

	if cfg.Driver != DriverSQLite {
		t.Fatalf("expected sqlite driver, got %q", cfg.Driver)
	}
	if cfg.TablePrefix != "x_metric_" {
		t.Fatalf("table prefix option was not applied: %q", cfg.TablePrefix)
	}
	if cfg.AutoMigrate {
		t.Fatalf("auto migrate option was not applied")
	}
	if cfg.MaxOpenConns != 10 || cfg.MaxIdleConns != 2 {
		t.Fatalf("pool options were not applied: %#v", cfg)
	}
	if cfg.ConnMaxLifetime != 30*time.Minute || cfg.ConnectTimeout != 2*time.Second {
		t.Fatalf("timeout options were not applied: %#v", cfg)
	}
}

// TestSQLiteInDirBuildsFileConfig verifies SQLiteInDir file configuration.
//
// TestSQLiteInDirBuildsFileConfig 验证 SQLiteInDir 会生成预期的文件数据库配置。
func TestSQLiteInDirBuildsFileConfig(t *testing.T) {
	cfg := SQLiteInDir(
		"data/metrics",
		WithSQLiteProfile(SQLiteProfilePerformance),
		WithSQLiteCacheSizeKB(128*1024),
	)

	if cfg.Driver != DriverSQLite {
		t.Fatalf("expected sqlite driver, got %q", cfg.Driver)
	}
	if cfg.MaxOpenConns != 1 || cfg.MaxIdleConns != 1 {
		t.Fatalf("sqlite directory config should default to a single connection: %#v", cfg)
	}
	if cfg.SQLite.PerformanceProfile != SQLiteProfilePerformance {
		t.Fatalf("sqlite profile option was not applied: %q", cfg.SQLite.PerformanceProfile)
	}
	if cfg.SQLite.CacheSizeKB != 128*1024 {
		t.Fatalf("sqlite cache option was not applied: %d", cfg.SQLite.CacheSizeKB)
	}
	if cfg.DSN != "file:data/metrics/metrics.db?mode=rwc&_txlock=immediate" {
		t.Fatalf("unexpected sqlite dir dsn: %q", cfg.DSN)
	}
}

func TestSQLiteDefaultsKeepCacheSmallAndDisableMmap(t *testing.T) {
	cfg := DefaultConfig(DriverSQLite, "file:metrics.db")
	if cfg.SQLite.CacheSizeKB != 4*1024 {
		t.Fatalf("default sqlite cache = %d KiB, want 4 MiB", cfg.SQLite.CacheSizeKB)
	}
	if cfg.SQLite.MMapSizeBytes != 0 {
		t.Fatalf("default sqlite mmap = %d, want 0", cfg.SQLite.MMapSizeBytes)
	}
	prepared, err := prepareSQLiteConfig(cfg)
	if err != nil {
		t.Fatalf("prepare sqlite config: %v", err)
	}
	if prepared.SQLite.CacheSizeKB != 4*1024 || prepared.SQLite.MMapSizeBytes != 0 {
		t.Fatalf("prepared sqlite config inflated memory: cache=%d mmap=%d", prepared.SQLite.CacheSizeKB, prepared.SQLite.MMapSizeBytes)
	}
}

func TestMySQLDSNHardeningRejectsAllowAllFilesAndForcesSafeFlags(t *testing.T) {
	rejected := []string{
		"user:pass@tcp(127.0.0.1:3306)/metrics?allowAllFiles=true",
		"user:pass@tcp(127.0.0.1:3306)/metrics?allowAllFiles=1",
		"user:pass@tcp(127.0.0.1:3306)/metrics?allowAllFiles=false",
		"user:pass@tcp(127.0.0.1:3306)/metrics?ALLOWALLFILES=true",
		"user:pass@tcp(127.0.0.1:3306)/metrics?charset=utf8mb4&allowAllFiles=true",
	}
	for _, rawDSN := range rejected {
		cfg := MySQL(rawDSN)
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Validate(%q) error = %v, want ErrInvalidArgument", rawDSN, err)
		}
		if _, err := Open(context.Background(), cfg); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Open(%q) error = %v, want ErrInvalidArgument", rawDSN, err)
		}
	}

	sanitized, err := sanitizeMySQLDSN("user:pass@tcp(127.0.0.1:3306)/metrics?allowOldPasswords=true&parseTime=true")
	if err != nil {
		t.Fatalf("sanitizeMySQLDSN returned unexpected error: %v", err)
	}
	parsed, err := mysql.ParseDSN(sanitized)
	if err != nil {
		t.Fatalf("ParseDSN(sanitized) failed: %v", err)
	}
	if parsed.AllowAllFiles {
		t.Fatalf("expected AllowAllFiles=false after sanitization")
	}
	if parsed.AllowOldPasswords {
		t.Fatalf("expected AllowOldPasswords=false after sanitization")
	}
}

func TestDefinitionsCacheRejectsStaleReloadAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, SQLite("file:defs_cache_gen_test?mode=memory&cache=shared"))
	if err != nil {
		t.Fatalf("open sqlite metric store: %v", err)
	}
	defer store.Close()

	if err := store.UpsertMetric(ctx, Definition{
		Name:          "cpu.usage",
		Type:          TypeGauge,
		Unit:          "percent",
		RetentionDays: 30,
	}); err != nil {
		t.Fatalf("UpsertMetric: %v", err)
	}
	store.invalidateDefinitionsCache()

	// Simulate the exact TOCTOU window: Reader queries SQLite (sees RetentionDays=30),
	// then BEFORE Reader takes defsMu.Lock(), a concurrent retention update commits 7
	// and calls invalidateDefinitionsCache(). Reader MUST NOT poison defsCache with 30!
	hookFired := false
	store.testAfterDefinitionsQuery = func() {
		if hookFired {
			return
		}
		hookFired = true
		store.testAfterDefinitionsQuery = nil
		if _, err := store.db.ExecContext(ctx,
			`UPDATE metric_definitions SET retention_days = 7 WHERE name = 'cpu.usage'`,
		); err != nil {
			t.Errorf("concurrent retention update failed: %v", err)
		}
		store.invalidateDefinitionsCache()
	}

	got, err := store.GetMetric(ctx, "cpu.usage")
	if err != nil {
		t.Fatalf("GetMetric during concurrent invalidation: %v", err)
	}
	if !hookFired {
		t.Fatal("expected testAfterDefinitionsQuery hook to fire")
	}
	if got.RetentionDays != 7 {
		t.Fatalf("in-flight GetMetric returned stale retention_days=%d, want 7", got.RetentionDays)
	}

	cached, err := store.GetMetric(ctx, "cpu.usage")
	if err != nil {
		t.Fatalf("subsequent GetMetric: %v", err)
	}
	if cached.RetentionDays != 7 {
		t.Fatalf("stale reload poisoned defsCache: retention_days=%d, want 7", cached.RetentionDays)
	}
}
