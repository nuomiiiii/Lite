package dbcore

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBuildSQLiteReadDSN(t *testing.T) {
	_, ok := buildSQLiteReadDSN(":memory:")
	require.False(t, ok)

	_, ok = buildSQLiteReadDSN("file:test?mode=memory&cache=shared")
	require.False(t, ok)

	filePath := filepath.Join(t.TempDir(), "lite.db")
	dsn, ok := buildSQLiteReadDSN(filePath)
	require.True(t, ok)
	require.Contains(t, dsn, "mode=ro")
	require.Contains(t, dsn, "_query_only=1")
	require.Contains(t, dsn, "_busy_timeout=5000")
}

func TestSQLiteReadPoolSeesWALWritesAndBlocksMutations(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "lite-wal.db")
	writeDB, err := gorm.Open(sqlite.Open(filePath), &gorm.Config{})
	require.NoError(t, err)
	writeSQL, err := writeDB.DB()
	require.NoError(t, err)
	defer writeSQL.Close()

	require.NoError(t, writeDB.Exec("PRAGMA journal_mode=WAL;").Error)
	require.NoError(t, writeDB.Exec("CREATE TABLE wal_items (id INTEGER PRIMARY KEY, val TEXT);").Error)
	require.NoError(t, writeDB.Exec("INSERT INTO wal_items (id, val) VALUES (1, 'initial');").Error)

	readDSN, ok := buildSQLiteReadDSN(filePath)
	require.True(t, ok)
	readDB, err := gorm.Open(sqlite.Open(readDSN), &gorm.Config{})
	require.NoError(t, err)
	readSQL, err := readDB.DB()
	require.NoError(t, err)
	defer readSQL.Close()

	var val string
	require.NoError(t, readDB.Raw("SELECT val FROM wal_items WHERE id = 1").Scan(&val).Error)
	require.Equal(t, "initial", val)

	// New WAL write committed on writer must be immediately visible on read-only pool.
	require.NoError(t, writeDB.Exec("INSERT INTO wal_items (id, val) VALUES (2, 'after-wal');").Error)
	require.NoError(t, readDB.Raw("SELECT val FROM wal_items WHERE id = 2").Scan(&val).Error)
	require.Equal(t, "after-wal", val)

	// Read-only pool must reject writes.
	err = readDB.Exec("INSERT INTO wal_items (id, val) VALUES (3, 'should-fail');").Error
	require.Error(t, err)
}
