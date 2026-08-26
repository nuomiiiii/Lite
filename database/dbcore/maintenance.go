package dbcore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/nuomiiiii/lite/cmd/flags"
)

var maintenanceMu sync.Mutex

// SQLiteFileSizes separates the persistent database file from runtime
// sidecars. WAL and SHM are real disk usage, but they fluctuate independently
// from the retained database contents.
type SQLiteFileSizes struct {
	Database int64
	WAL      int64
	SHM      int64
}

func (sizes SQLiteFileSizes) Total() int64 {
	return sizes.Database + sizes.WAL + sizes.SHM
}

// StorageSize returns the bytes occupied by the main SQLite database and its
// WAL/SHM sidecar files.
func StorageSize() (int64, error) {
	files, err := StorageFiles()
	if err != nil {
		return 0, err
	}
	return files.Total(), nil
}

// StorageFiles returns the physical files used by the main SQLite database.
func StorageFiles() (SQLiteFileSizes, error) {
	if !flags.IsSQLite() {
		return SQLiteFileSizes{}, errors.New("main database size is only available for SQLite")
	}
	return sqliteFileSetSizes(resolveDatabaseFile())
}

// ReclaimSpace checkpoints the main database WAL and rewrites the SQLite file.
func ReclaimSpace(ctx context.Context) error {
	if !flags.IsSQLite() {
		return errors.New("main database maintenance is only supported for SQLite")
	}

	maintenanceMu.Lock()
	defer maintenanceMu.Unlock()

	db, err := GetDBInstance().DB()
	if err != nil {
		return fmt.Errorf("get main database connection: %w", err)
	}
	if err := checkpointSQLiteWAL(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum main database: %w", err)
	}
	return checkpointSQLiteWAL(ctx, db)
}

func checkpointSQLiteWAL(ctx context.Context, db *sql.DB) error {
	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("checkpoint main database WAL: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf(
			"checkpoint main database WAL: database is busy (%d log frames, %d checkpointed)",
			logFrames,
			checkpointedFrames,
		)
	}
	return nil
}

func sqliteFileSetSize(dsn string) (int64, error) {
	files, err := sqliteFileSetSizes(dsn)
	if err != nil {
		return 0, err
	}
	return files.Total(), nil
}

func sqliteFileSetSizes(dsn string) (SQLiteFileSizes, error) {
	path := strings.TrimPrefix(strings.TrimSpace(dsn), "file:")
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	if path == "" || path == ":memory:" || strings.Contains(strings.ToLower(dsn), "mode=memory") {
		return SQLiteFileSizes{}, errors.New("database is not backed by a local file")
	}

	var sizes SQLiteFileSizes
	foundDatabase := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		switch {
		case err == nil:
			switch suffix {
			case "":
				sizes.Database = info.Size()
				foundDatabase = true
			case "-wal":
				sizes.WAL = info.Size()
			case "-shm":
				sizes.SHM = info.Size()
			}
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return SQLiteFileSizes{}, fmt.Errorf("stat database file %q: %w", path+suffix, err)
		}
	}
	if !foundDatabase {
		return SQLiteFileSizes{}, fmt.Errorf("database file %q does not exist", path)
	}
	return sizes, nil
}
