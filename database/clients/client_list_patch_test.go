package clients

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestClientBasicInfoCachePatchUpdatesSingleNodeWithoutInvalidating(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:client_patch_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)

	now := time.Now()
	cache := &clientBasicInfoCache{}
	gen := cache.generation(db)
	cache.set(db, gen, now, []models.Client{
		{UUID: "n-1", Name: "Node-1", DeploymentStatus: DeploymentDeliverySaved},
		{UUID: "n-2", Name: "Node-2", DeploymentStatus: DeploymentDeliveryApplied},
	})

	patched := cache.patch(db, "n-1", now.Add(time.Second), clientListCacheTTL, func(c *models.Client) {
		c.DeploymentStatus = DeploymentDeliverySent
		c.MCPFull = true
		c.MCPFullVersion = 2
	})
	require.True(t, patched)
	require.True(t, cache.validForDB(db))

	list, ok := cache.get(db, now.Add(2*time.Second), clientListCacheTTL)
	require.True(t, ok)
	require.Len(t, list, 2)
	require.Equal(t, DeploymentDeliverySent, list[0].DeploymentStatus)
	require.True(t, list[0].MCPFull)
	require.Equal(t, 2, list[0].MCPFullVersion)
	require.Equal(t, DeploymentDeliveryApplied, list[1].DeploymentStatus)

	// Missing UUID returns false so caller can fall back to full invalidation.
	require.False(t, cache.patch(db, "missing", now.Add(2*time.Second), clientListCacheTTL, func(c *models.Client) {}))

	// Expired cache entry must not be patched; returning false lets caller invalidate cleanly.
	genBeforeExpiredPatch := cache.generation(db)
	require.False(t, cache.patch(db, "n-1", now.Add(clientListCacheTTL+time.Second), clientListCacheTTL, func(c *models.Client) {
		c.MCPFullVersion = 99
	}))
	require.Equal(t, genBeforeExpiredPatch, cache.generation(db), "expired patch must not bump generation")
}
