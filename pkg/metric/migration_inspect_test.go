package metric

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteMigrationCompatibilityMatrix(t *testing.T) {
	versions := []struct {
		name   string
		layout string
	}{
		{name: "2.1.x-v3", layout: "normalized"},
		{name: "2.1.8", layout: "v4"},
		{name: "2.1.8-fix", layout: "v4"},
	}

	for _, version := range versions {
		t.Run(version.name, func(t *testing.T) {
			ctx := context.Background()
			dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
			base := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			wantBits := math.Float64bits(73.125)

			var store *Store
			var err error
			if version.layout == "normalized" {
				store = createSQLiteV3OnlyStore(t, ctx, dsn)
			} else {
				store, err = Open(ctx, SQLite(dsn))
				if err != nil {
					t.Fatalf("create %s V4 fixture: %v", version.name, err)
				}
			}
			if err := store.CreateMetric(ctx, Definition{Name: "compat.value", Type: TypeGauge, RetentionDays: 10}); err != nil {
				t.Fatalf("create %s metric: %v", version.name, err)
			}
			if err := store.Write(ctx, Point{
				MetricName: "compat.value",
				EntityID:   "node-a",
				Timestamp:  base,
				Value:      math.Float64frombits(wantBits),
				Tags:       map[string]string{"source": version.name},
				Labels:     map[string]string{"precision": "bit-exact"},
			}); err != nil {
				t.Fatalf("write %s metric: %v", version.name, err)
			}
			if version.layout == "v4" {
				if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 0`); err != nil {
					t.Fatalf("clear %s migration marker: %v", version.name, err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close %s fixture: %v", version.name, err)
			}

			summary, err := InspectSQLiteMigration(ctx, SQLite(dsn))
			if err != nil {
				t.Fatalf("inspect %s fixture: %v", version.name, err)
			}
			if !summary.Required || summary.Layout != version.layout {
				t.Fatalf("%s should enter the migration page: %#v", version.name, summary)
			}
			if version.layout == "v4" && !summary.DigestHandoffRequired {
				t.Fatalf("%s digest handoff was not detected: %#v", version.name, summary)
			}

			var phases []string
			store, err = Open(ctx, SQLite(dsn, WithMigrationProgress(func(progress MigrationProgress) {
				phases = append(phases, progress.Phase)
			})))
			if err != nil {
				t.Fatalf("upgrade %s: %v", version.name, err)
			}
			definition, err := store.GetMetric(ctx, "compat.value")
			if err != nil || definition.RetentionDays != 10 {
				t.Fatalf("%s retention changed: definition=%#v err=%v", version.name, definition, err)
			}
			points, err := store.Query(ctx, Query{
				MetricName: "compat.value",
				EntityID:   "node-a",
				Start:      base,
				End:        base,
				Tags:       map[string]string{"source": version.name},
			})
			if err != nil || len(points) != 1 {
				t.Fatalf("read %s metric after migration: points=%#v err=%v", version.name, points, err)
			}
			if math.Float64bits(points[0].Value) != wantBits || points[0].Labels["precision"] != "bit-exact" {
				t.Fatalf("%s precision changed during migration: %#v", version.name, points[0])
			}
			if len(phases) == 0 || phases[len(phases)-1] != MigrationPhaseCompleted {
				t.Fatalf("%s migration progress did not complete: %v", version.name, phases)
			}
			var userVersion int
			if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
				t.Fatalf("read %s migration marker: %v", version.name, err)
			}
			if userVersion < sqliteStorageVersionV4DigestHandoff {
				t.Fatalf("%s migration marker=%d want >=%d", version.name, userVersion, sqliteStorageVersionV4DigestHandoff)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close upgraded %s store: %v", version.name, err)
			}

			summary, err = InspectSQLiteMigration(ctx, SQLite(dsn))
			if err != nil {
				t.Fatalf("inspect upgraded %s database: %v", version.name, err)
			}
			if summary.Required || summary.Layout != "current" || summary.DigestHandoffRequired {
				t.Fatalf("%s migration should not repeat: %#v", version.name, summary)
			}
		})
	}
}

func TestSQLiteMigrationInspectorLeavesEmptyDatabaseOnNormalStartup(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	summary, err := InspectSQLiteMigration(ctx, SQLite(dsn))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Required || summary.Layout != "empty" {
		t.Fatalf("new database should not enter migration mode: %#v", summary)
	}

	store, err := Open(ctx, SQLite(dsn))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", sqliteFilePath(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if userVersion < sqliteStorageVersionV4DigestHandoff {
		t.Fatalf("new V4 database marker=%d want >=%d", userVersion, sqliteStorageVersionV4DigestHandoff)
	}
}
