package managedconfig

import (
	"path/filepath"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSelectorDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "selectors.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Client{}, &models.PingTask{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestSelectorDecodersAcceptArraysAndLegacyJSON(t *testing.T) {
	require.Equal(t, []string{"node-a", "node-b"}, NodeIDs([]any{"node-a", "node-a", "node-b"}))
	require.Equal(t, []string{"node-a", "node-b"}, NodeIDs(`["node-a","node-b"]`))
	require.Empty(t, NodeIDs(`not-json`))
	require.Equal(t, []uint{1, 2}, PingTaskIDs([]any{1.0, 1.0, 2.0}))
	require.Equal(t, []uint{1, 2}, PingTaskIDs(`[1,2]`))
	require.Empty(t, PingTaskIDs(`{"wrong":true}`))
}

func TestResolveForOutputFiltersDeletedReferences(t *testing.T) {
	db := openSelectorDB(t)
	require.NoError(t, db.Create(&models.Client{UUID: "node-a", Token: "token-a", Name: "A"}).Error)
	require.NoError(t, db.Create(&models.Client{UUID: "node-b", Token: "token-b", Name: "B"}).Error)
	require.NoError(t, db.Create(&models.PingTask{Id: 3, Name: "Task 3", Target: "example.com"}).Error)
	require.NoError(t, db.Create(&models.PingTask{Id: 4, Name: "Task 4", Target: "example.net"}).Error)

	values := map[string]any{
		"visibleNodes": `["node-a","deleted-node","node-b"]`,
		"visibleTasks": []any{3.0, 99.0, 4.0},
		"unchanged":    "value",
	}
	items := []models.ManagedThemeConfigurationItem{
		{Key: "visibleNodes", Type: TypeNodes},
		{Key: "visibleTasks", Type: TypePingTasks},
	}
	require.NoError(t, ResolveForOutput(db, values, items))
	require.Equal(t, []string{"node-a", "node-b"}, values["visibleNodes"])
	require.Equal(t, []uint{3, 4}, values["visibleTasks"])
	require.Equal(t, "value", values["unchanged"])
}

func TestSelectorDefaultsAreStructuredArrays(t *testing.T) {
	require.Equal(t, []string{}, DefaultValue(models.ManagedThemeConfigurationItem{Type: TypeNodes}))
	require.Equal(t, []uint{}, DefaultValue(models.ManagedThemeConfigurationItem{Type: TypePingTasks}))
	require.Equal(t, []string{"node-a"}, DefaultValue(models.ManagedThemeConfigurationItem{Type: TypeNodes, Default: `["node-a"]`}))
}
