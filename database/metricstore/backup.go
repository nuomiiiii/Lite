package metricstore

import (
	"context"
	"fmt"

	"github.com/komari-monitor/komari/pkg/metric"
)

// BackupSQLite keeps the active Store stable while producing a consistent
// point-in-time snapshot of all raw, rollup, digest, and migration data.
func BackupSQLite(ctx context.Context, destination string) error {
	if err := storeOperations.AcquireShared(ctx); err != nil {
		return fmt.Errorf("wait for metric store before backup: %w", err)
	}
	defer storeOperations.ReleaseShared()

	storeMu.RLock()
	defer storeMu.RUnlock()
	activeStore := store
	if activeStore == nil {
		return fmt.Errorf("metric store not initialized")
	}
	if activeStore.Driver() != metric.DriverSQLite {
		return fmt.Errorf("complete historical backup is only available for the SQLite metric store")
	}
	return activeStore.BackupSQLite(ctx, destination)
}
