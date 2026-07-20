package metricstore

import (
	"testing"
	"time"
)

func TestStartupStoreContextAllowsLongSQLiteMigration(t *testing.T) {
	ctx, cancel := startupStoreContext(&MetricStoreConfig{
		Driver: "sqlite",
		DSN:    "./data/metrics.db",
	})
	defer cancel()

	if deadline, ok := ctx.Deadline(); ok {
		t.Fatalf("SQLite startup migration has unexpected deadline %v", deadline)
	}
}

func TestStartupStoreContextBoundsRemoteInitialization(t *testing.T) {
	ctx, cancel := startupStoreContext(&MetricStoreConfig{
		Driver: "postgresql",
		DSN:    "postgres://localhost/komari",
	})
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("remote metric store initialization has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > externalStoreInitTimeout {
		t.Fatalf("remote initialization deadline remaining = %v", remaining)
	}
}
