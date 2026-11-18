package clients

import (
	"fmt"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetClientsByUUIDsQueryCountAndMissing(t *testing.T) {
	cases := []struct {
		name    string
		count   int
		queries int
	}{
		{name: "100", count: 100, queries: 1},
		{name: "500", count: 500, queries: 2},
		{name: "1000", count: 1000, queries: 3},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db := newClientTestDB(t, "uuid-lookup-"+test.name)
			rows := make([]models.Client, 0, test.count)
			uuids := make([]string, 0, test.count+1)
			for i := 0; i < test.count; i++ {
				uuid := fmt.Sprintf("node-%s-%04d", test.name, i)
				rows = append(rows, models.Client{
					UUID:                 uuid,
					Token:                fmt.Sprintf("token-%s-%04d", test.name, i),
					RemoteProtocol:       2,
					RemoteControlEnabled: true,
					MCPFull:              true,
					MCPFullVersion:       1,
				})
				uuids = append(uuids, uuid)
			}
			for i := 0; i < len(rows); i += uuidQueryChunkSize {
				end := i + uuidQueryChunkSize
				if end > len(rows) {
					end = len(rows)
				}
				require.NoError(t, db.Create(rows[i:end]).Error)
			}
			uuids = append(uuids, "missing-"+test.name)

			var queries int
			callbackName := "test:count_uuid_lookup_" + test.name
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(*gorm.DB) {
				queries++
			}))
			t.Cleanup(func() { db.Callback().Query().Remove(callbackName) })

			found, err := getClientsByUUIDs(db, append(uuids, uuids[0]))
			require.NoError(t, err)
			require.Equal(t, test.count, len(found))
			_, missing := found["missing-"+test.name]
			require.False(t, missing)
			require.Equal(t, test.queries, queries)
			require.Equal(t, 2, found[uuids[0]].RemoteProtocol)
			require.True(t, found[uuids[0]].RemoteControlEnabled)
			require.True(t, found[uuids[0]].MCPFull)
			require.Equal(t, 1, found[uuids[0]].MCPFullVersion)
		})
	}
}

func TestTokenUUIDCacheRejectsStaleGenerationAndHonorsTTL(t *testing.T) {
	db := newClientTestDB(t, "token-cache-gen")
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&models.Client{
		UUID:  "node-gen-1",
		Token: "tok-initial",
	}).Error)

	// Capture generation before simulated concurrent invalidation (e.g. token rotation / delete).
	staleGen := tokenUUIDCache.generation(db)
	invalidateClientListCache()
	tokenUUIDCache.set(db, staleGen, "tok-initial", "node-gen-1", now)
	_, ok := tokenUUIDCache.get(db, "tok-initial", now)
	require.False(t, ok, "stale generation set must not populate tokenUUIDCache")

	// Normal lookup populates cache and respects TTL.
	gotUUID, err := getClientUUIDByToken(db, "tok-initial", now)
	require.NoError(t, err)
	require.Equal(t, "node-gen-1", gotUUID)

	cachedUUID, ok := tokenUUIDCache.get(db, "tok-initial", now.Add(clientTokenCacheTTL-time.Second))
	require.True(t, ok)
	require.Equal(t, "node-gen-1", cachedUUID)

	_, ok = tokenUUIDCache.get(db, "tok-initial", now.Add(clientTokenCacheTTL))
	require.False(t, ok, "expired token cache entry must miss")
}

func TestApplyClientDisplayFieldsAndPersistInvalidatesListCache(t *testing.T) {
	db := newClientTestDB(t, "persist-display-invalidate")
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	resetDay := 1
	client := models.Client{
		UUID:                  "node-disp-1",
		Token:                 "tok-disp-1",
		TrafficResetDay:       &resetDay,
		TrafficResetAllowance: 1024,
		TrafficResetCycle:     "1999-01", // stale cycle triggers DB update
	}
	require.NoError(t, db.Create(&client).Error)

	staleBasicGen := basicInfoCache.generation(db)
	invalidateClientListCache()
	basicInfoCache.set(db, staleBasicGen, now, []models.Client{client})
	_, ok := basicInfoCache.get(db, now, clientListCacheTTL)
	require.False(t, ok, "stale generation set must not populate basicInfoCache")

	gen := basicInfoCache.generation(db)
	basicInfoCache.set(db, gen, now, []models.Client{client})
	_, ok = basicInfoCache.get(db, now, clientListCacheTTL)
	require.True(t, ok)

	require.NoError(t, applyClientDisplayFieldsAndPersist(db, []models.Client{client}, now))
	_, ok = basicInfoCache.get(db, now, clientListCacheTTL)
	require.False(t, ok, "basicInfoCache must be invalidated when traffic display fields persist")
}
