package accounts

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuthReadCacheInvalidatesImmediatelyOnRevocationAndUserChange(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:auth_cache_test?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.Session{}))

	now := time.Now().UTC()
	cache := &authReadCache{}
	gen := cache.generation(db)

	session := models.Session{
		UUID:         "u-1",
		Session:      "hashed-session-1",
		Expires:      now.Add(time.Hour),
		LatestOnline: now,
	}
	user := models.User{
		UUID:     "u-1",
		Username: "admin",
	}

	cache.setSession(db, gen, "hashed-session-1", session, now)
	cache.setUser(db, gen, "u-1", user, now)

	gotSession, ok := cache.getSession(db, "hashed-session-1", now.Add(time.Second))
	require.True(t, ok)
	require.Equal(t, "u-1", gotSession.UUID)

	gotUser, ok := cache.getUser(db, "u-1", now.Add(time.Second))
	require.True(t, ok)
	require.Equal(t, "admin", gotUser.Username)

	// Expired entries are never returned.
	_, ok = cache.getSession(db, "hashed-session-1", now.Add(authCacheTTL+time.Second))
	require.False(t, ok)
	_, ok = cache.getUser(db, "u-1", now.Add(authCacheTTL+time.Second))
	require.False(t, ok)

	// Generation mismatch (TOCTOU) prevents stale write-back after invalidation.
	staleGen := cache.generation(db)
	cache.mu.Lock()
	cache.gen++
	cache.sessions = nil
	cache.users = nil
	cache.mu.Unlock()
	cache.setSession(db, staleGen, "hashed-session-1", session, now)
	cache.setUser(db, staleGen, "u-1", user, now)
	_, ok = cache.getSession(db, "hashed-session-1", now)
	require.False(t, ok)
	_, ok = cache.getUser(db, "u-1", now)
	require.False(t, ok)
}

func TestGetSessionCachesHitAndDeleteSessionInvalidatesImmediately(t *testing.T) {
	username := "cache-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "correctpassword")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = DeleteAllSessions()
		_ = DeleteAccountByUsername(username)
	})

	plain, err := CreateSession(user.UUID, int(time.Hour/time.Second), "ua", "127.0.0.1", "password")
	require.NoError(t, err)

	// First lookup populates the session read cache.
	gotUUID, err := GetSession(plain)
	require.NoError(t, err)
	require.Equal(t, user.UUID, gotUUID)

	// Verify entry is present in authCache.
	_, cached := authCache.getSession(dbcore.GetDBInstance(), SessionLookupKey(plain), time.Now().UTC())
	require.True(t, cached, "GetSession should populate authCache")

	// DeleteSession must immediately invalidate both DB and cache.
	require.NoError(t, DeleteSession(plain))
	_, cached = authCache.getSession(dbcore.GetDBInstance(), SessionLookupKey(plain), time.Now().UTC())
	require.False(t, cached, "DeleteSession must evict session from authCache")

	_, err = GetSession(plain)
	require.Error(t, err, "deleted session must be rejected immediately")
}
