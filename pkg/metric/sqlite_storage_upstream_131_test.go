package metric

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteStorageMigratesUpstream131RollupsToV4(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	seedUpstream131SQLiteStore(t, dsn, base, false)

	summary, err := InspectSQLiteMigration(ctx, SQLite(dsn))
	if err != nil {
		t.Fatalf("inspect upstream 1.3.1 database: %v", err)
	}
	if !summary.Required || summary.Layout != "upstream-1.3.1" || summary.SourceRows != 2 {
		t.Fatalf("upstream 1.3.1 should enter the migration page: %#v", summary)
	}

	var phases []string
	store, err := Open(ctx, SQLite(dsn, WithMigrationProgress(func(progress MigrationProgress) {
		phases = append(phases, progress.Phase)
	})))
	if err != nil {
		t.Fatalf("upgrade upstream 1.3.1 database: %v", err)
	}
	defer store.Close()

	definition, err := store.GetMetric(ctx, "compat.131")
	if err != nil {
		t.Fatalf("read migrated definition: %v", err)
	}
	if definition.RetentionDays != 17 {
		t.Fatalf("upstream 1.3.1 retention changed: got=%d want=17", definition.RetentionDays)
	}
	if !definition.CreatedAt.Equal(base) || !definition.UpdatedAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("upstream 1.3.1 definition times changed: created=%v updated=%v", definition.CreatedAt, definition.UpdatedAt)
	}

	tags := map[string]string{"task_id": "7"}
	rows, err := store.scanRollupRowsBetween(ctx, "compat.131", "upstream-node", tags,
		time.Minute.Nanoseconds(), base.UnixNano(), base.UnixNano(), true)
	if err != nil {
		t.Fatalf("read migrated upstream 1.3.1 rollup: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("migrated rollup rows=%d want=1", len(rows))
	}
	bucket := rows[0].bucketData
	if bucket.count != 2 || bucket.sum != 30 || bucket.sumSq != 500 || bucket.min != 10 || bucket.max != 20 {
		t.Fatalf("migrated rollup summary changed: %#v", bucket)
	}
	if got := bucket.digest.Quantile(0.5); got < 10 || got > 20 {
		t.Fatalf("migrated digest is invalid: p50=%v", got)
	}
	if len(phases) == 0 || phases[len(phases)-1] != MigrationPhaseCompleted {
		t.Fatalf("upstream 1.3.1 migration progress did not complete: %v", phases)
	}
	foundNormalize := false
	for _, phase := range phases {
		if phase == MigrationPhaseNormalizingRollups {
			foundNormalize = true
			break
		}
	}
	if !foundNormalize {
		t.Fatalf("upstream 1.3.1 conversion was not visible in migration progress: %v", phases)
	}

	summary, err = InspectSQLiteMigration(ctx, SQLite(dsn))
	if err != nil {
		t.Fatalf("inspect migrated upstream 1.3.1 database: %v", err)
	}
	if summary.Required || summary.Layout != "current" {
		t.Fatalf("upstream 1.3.1 migration should not repeat: %#v", summary)
	}
}

func TestSQLiteStorageMigratesUpstream14RollupsToV4(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	seedUpstreamSQLiteStore(t, dsn, base, false, "metric_label_sets")

	summary, err := InspectSQLiteMigration(ctx, SQLite(dsn))
	if err != nil {
		t.Fatalf("inspect upstream 1.4.x database: %v", err)
	}
	if !summary.Required || summary.Layout != "upstream-1.4.x" || summary.SourceRows != 2 {
		t.Fatalf("upstream 1.4.x should enter the migration page: %#v", summary)
	}

	store, err := Open(ctx, SQLite(dsn))
	if err != nil {
		t.Fatalf("upgrade upstream 1.4.x database: %v", err)
	}
	defer store.Close()

	rows, err := store.scanRollupRowsBetween(ctx, "compat.131", "upstream-node", map[string]string{"task_id": "7"},
		time.Minute.Nanoseconds(), base.UnixNano(), base.UnixNano(), true)
	if err != nil {
		t.Fatalf("read migrated upstream 1.4.x rollup: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("migrated upstream 1.4.x rollup rows=%d want=1", len(rows))
	}
	bucket := rows[0].bucketData
	if bucket.count != 2 || bucket.sum != 30 || bucket.sumSq != 500 || bucket.min != 10 || bucket.max != 20 {
		t.Fatalf("migrated upstream 1.4.x rollup summary changed: %#v", bucket)
	}
	if got := bucket.digest.Quantile(0.5); got < 10 || got > 20 {
		t.Fatalf("migrated upstream 1.4.x digest is invalid: p50=%v", got)
	}
}

func TestSQLiteStorageUpstream131OverflowRollsBack(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	seedUpstream131SQLiteStore(t, dsn, base, true)

	store, err := Open(ctx, SQLite(dsn))
	if err == nil {
		_ = store.Close()
		t.Fatal("upstream 1.3.1 migration unexpectedly accepted an overflowing timestamp")
	}

	db, openErr := sql.Open("sqlite3", dsn)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	matched, inspectErr := inspectSQLiteUpstream131Schema(ctx, db, tables{
		definitions: "metric_definitions",
		series:      "metric_series",
		labels:      "metric_labels",
		resolutions: "metric_resolutions",
		rollups:     "metric_rollups",
	})
	if inspectErr != nil || !matched {
		t.Fatalf("failed migration did not preserve the upstream 1.3.1 schema: matched=%v err=%v", matched, inspectErr)
	}
	var staging int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_upstream_131_source'`).Scan(&staging); err != nil {
		t.Fatal(err)
	}
	if staging != 0 {
		t.Fatalf("failed upstream 1.3.1 migration left %d staging objects", staging)
	}
}

func TestSQLiteStorageUpstream14OverflowRollsBack(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	seedUpstreamSQLiteStore(t, dsn, base, true, "metric_label_sets")

	store, err := Open(ctx, SQLite(dsn))
	if err == nil {
		_ = store.Close()
		t.Fatal("upstream 1.4.x migration unexpectedly accepted an overflowing timestamp")
	}

	db, openErr := sql.Open("sqlite3", dsn)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	layout, inspectErr := inspectSQLiteUpstreamSchema(ctx, db, tables{
		definitions: "metric_definitions",
		series:      "metric_series",
		labels:      "metric_labels",
		resolutions: "metric_resolutions",
		rollups:     "metric_rollups",
	})
	if inspectErr != nil || layout.name != "upstream-1.4.x" {
		t.Fatalf("failed migration did not preserve the upstream 1.4.x schema: layout=%#v err=%v", layout, inspectErr)
	}
	var staging int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_upstream_131_source'`).Scan(&staging); err != nil {
		t.Fatal(err)
	}
	if staging != 0 {
		t.Fatalf("failed upstream 1.4.x migration left %d staging objects", staging)
	}
}

func TestSQLiteStorageRejectsAmbiguousUpstreamLayouts(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute)
	seedUpstreamSQLiteStore(t, dsn, base, false, "metric_label_sets")

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE metric_labels (
		id INTEGER PRIMARY KEY AUTOINCREMENT, labels_hash VARCHAR(64) NOT NULL, labels TEXT NOT NULL,
		UNIQUE(labels_hash))`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = InspectSQLiteMigration(ctx, SQLite(dsn))
	if err == nil || !strings.Contains(err.Error(), "ambiguous upstream SQLite layouts") {
		t.Fatalf("ambiguous upstream schemas were not rejected: %v", err)
	}
}

func TestDecodeUpstream131DigestRebuildsConstantBucket(t *testing.T) {
	digest, err := decodeUpstream131Digest(upstream131RollupRow{
		count: 8,
		min:   42,
		max:   42,
	})
	if err != nil {
		t.Fatalf("rebuild constant digest: %v", err)
	}
	if got := digest.Quantile(0.99); got != 42 {
		t.Fatalf("constant digest p99 = %v, want 42", got)
	}
}

func TestDecodeUpstream131DigestRejectsMissingVariableBucket(t *testing.T) {
	_, err := decodeUpstream131Digest(upstream131RollupRow{
		count: 2,
		min:   10,
		max:   20,
	})
	if err == nil {
		t.Fatal("missing non-constant digest was accepted")
	}
}

func seedUpstream131SQLiteStore(t *testing.T, dsn string, base time.Time, overflow bool) {
	seedUpstreamSQLiteStore(t, dsn, base, overflow, "metric_labels")
}

func seedUpstreamSQLiteStore(t *testing.T, dsn string, base time.Time, overflow bool, labelTable string) {
	t.Helper()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE metric_definitions (
		 name VARCHAR(191) PRIMARY KEY, type VARCHAR(32) NOT NULL,
		 unit VARCHAR(64) NOT NULL DEFAULT '', description TEXT NOT NULL,
		 retention_days INTEGER NOT NULL DEFAULT 0, metadata TEXT NOT NULL,
		 created_at_milli BIGINT NOT NULL, updated_at_milli BIGINT NOT NULL)`,
		`CREATE TABLE ` + labelTable + ` (
		 id INTEGER PRIMARY KEY AUTOINCREMENT, labels_hash VARCHAR(64) NOT NULL, labels TEXT NOT NULL,
		 UNIQUE(labels_hash))`,
		`CREATE TABLE metric_series (
		 id INTEGER PRIMARY KEY AUTOINCREMENT, metric_name VARCHAR(191) NOT NULL, entity_id VARCHAR(191) NOT NULL,
		 tags_hash VARCHAR(64) NOT NULL, tags TEXT NOT NULL,
		 UNIQUE(metric_name, entity_id, tags_hash),
		 FOREIGN KEY (metric_name) REFERENCES metric_definitions(name) ON DELETE CASCADE)`,
		`CREATE TABLE metric_resolutions (
		 id INTEGER PRIMARY KEY AUTOINCREMENT, resolution_milli BIGINT NOT NULL, UNIQUE(resolution_milli))`,
		`CREATE TABLE metric_rollups (
		 series_id BIGINT NOT NULL, resolution_id BIGINT NOT NULL, label_id BIGINT NOT NULL, bucket_milli BIGINT NOT NULL,
		 count BIGINT NOT NULL, sum DOUBLE PRECISION NOT NULL, sum_sq DOUBLE PRECISION NOT NULL,
		 min_val DOUBLE PRECISION NOT NULL, max_val DOUBLE PRECISION NOT NULL,
		 first_val DOUBLE PRECISION NOT NULL, first_ts_milli BIGINT NOT NULL,
		 last_val DOUBLE PRECISION NOT NULL, last_ts_milli BIGINT NOT NULL,
		 digest BLOB, created_at_milli BIGINT NOT NULL,
		 UNIQUE(series_id, resolution_id, label_id, bucket_milli),
		 FOREIGN KEY (series_id) REFERENCES metric_series(id) ON DELETE CASCADE,
		 FOREIGN KEY (resolution_id) REFERENCES metric_resolutions(id) ON DELETE CASCADE,
		 FOREIGN KEY (label_id) REFERENCES ` + labelTable + `(id) ON DELETE CASCADE)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create upstream 1.3.1 fixture: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO metric_definitions
		(name, type, unit, description, retention_days, metadata, created_at_milli, updated_at_milli)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "compat.131", "gauge", "%", "compatibility", 17,
		`{"source":"upstream-1.3.1"}`, base.UnixMilli(), base.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	tagsHash, tagsJSON, err := tagsFingerprint(map[string]string{"task_id": "7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO metric_series (id, metric_name, entity_id, tags_hash, tags) VALUES (?, ?, ?, ?, ?)`,
		11, "compat.131", "upstream-node", tagsHash, tagsJSON); err != nil {
		t.Fatal(err)
	}
	resolution := time.Minute.Milliseconds()
	if overflow {
		resolution = math.MaxInt64
	}
	if _, err := db.Exec(`INSERT INTO metric_resolutions (id, resolution_milli) VALUES (?, ?)`, 21, resolution); err != nil {
		t.Fatal(err)
	}
	for index, labels := range []map[string]string{{"source": "a"}, {"source": "b"}} {
		labelHash, labelJSON, err := tagsFingerprint(labels)
		if err != nil {
			t.Fatal(err)
		}
		labelID := 31 + index
		if _, err := db.Exec(`INSERT INTO `+labelTable+` (id, labels_hash, labels) VALUES (?, ?, ?)`, labelID, labelHash, labelJSON); err != nil {
			t.Fatal(err)
		}
		value := float64((index + 1) * 10)
		digest := NewTDigest(30)
		digest.Add(value, 1)
		stamp := base.Add(time.Duration(index) * 20 * time.Second).UnixMilli()
		if _, err := db.Exec(`INSERT INTO metric_rollups
			(series_id, resolution_id, label_id, bucket_milli, count, sum, sum_sq, min_val, max_val,
			 first_val, first_ts_milli, last_val, last_ts_milli, digest, created_at_milli)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			11, 21, labelID, base.UnixMilli(), 1, value, value*value, value, value,
			value, stamp, value, stamp, digest.encodeRaw(), base.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
}
