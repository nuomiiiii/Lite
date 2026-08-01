package metric

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BackupSQLite writes a transactionally consistent, standalone SQLite
// snapshot. Committed writes may continue while SQLite builds the snapshot;
// WAL/SHM sidecars are not needed by the resulting file.
func (s *Store) BackupSQLite(ctx context.Context, destination string) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || s.db == nil {
		return ErrClosed
	}
	if s.cfg.Driver != DriverSQLite {
		return fmt.Errorf("%w: consistent file backup is only available for SQLite", ErrInvalidArgument)
	}

	absolute, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("metric: resolve SQLite backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return fmt.Errorf("metric: create SQLite backup directory: %w", err)
	}
	if err := os.Remove(absolute); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("metric: remove previous SQLite backup: %w", err)
	}

	safePath := strings.ReplaceAll(filepath.ToSlash(absolute), "'", "''")
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", safePath)); err != nil {
		return fmt.Errorf("metric: back up SQLite database: %w", err)
	}
	return nil
}
