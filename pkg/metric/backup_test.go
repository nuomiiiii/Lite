package metric

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupSQLiteIncludesCommittedMetricHistory(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "metrics.db")
	store, err := Open(ctx, SQLite(source))
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	defer store.Close()

	if err := store.UpsertMetric(ctx, Definition{Name: "cpu", Type: TypeGauge, RetentionDays: 15}); err != nil {
		t.Fatalf("create metric: %v", err)
	}
	stamp := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	if err := store.Write(ctx, Point{MetricName: "cpu", EntityID: "node-a", Timestamp: stamp, Value: 42.5}); err != nil {
		t.Fatalf("write point: %v", err)
	}

	destination := filepath.Join(t.TempDir(), "metrics.db")
	if err := store.BackupSQLite(ctx, destination); err != nil {
		t.Fatalf("back up store: %v", err)
	}

	db, err := sql.Open("sqlite3", destination)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		t.Fatalf("check snapshot: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("snapshot integrity = %q", integrity)
	}

	restored, err := Open(ctx, SQLite(destination))
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer restored.Close()
	points, err := restored.Query(ctx, Query{
		MetricName: "cpu",
		EntityID:   "node-a",
		Start:      stamp.Add(-time.Second),
		End:        stamp.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("query restored point: %v", err)
	}
	if len(points) != 1 || points[0].Value != 42.5 || !points[0].Timestamp.Equal(stamp) {
		t.Fatalf("restored points = %#v", points)
	}
}
