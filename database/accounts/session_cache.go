package accounts

import (
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

const (
	authCacheTTL      = 15 * time.Second
	authCacheMaxItems = 512
)

type cachedSessionEntry struct {
	record    models.Session
	expiresAt time.Time
}

type cachedUserEntry struct {
	user      models.User
	expiresAt time.Time
}

type authReadCache struct {
	mu       sync.RWMutex
	db       *gorm.DB
	gen      uint64
	sessions map[string]cachedSessionEntry
	users    map[string]cachedUserEntry
}

var authCache authReadCache

func (c *authReadCache) generation(db *gorm.DB) uint64 {
	c.mu.RLock()
	if c.db == db {
		gen := c.gen
		c.mu.RUnlock()
		return gen
	}
	c.mu.RUnlock()

	c.mu.Lock()
	if c.db != db {
		c.gen++
		c.db = db
		c.sessions = nil
		c.users = nil
	}
	gen := c.gen
	c.mu.Unlock()
	return gen
}

func (c *authReadCache) getSession(db *gorm.DB, key string, now time.Time) (models.Session, bool) {
	if db == nil || key == "" {
		return models.Session{}, false
	}
	c.mu.RLock()
	if c.db != db || c.sessions == nil {
		c.mu.RUnlock()
		return models.Session{}, false
	}
	entry, ok := c.sessions[key]
	c.mu.RUnlock()
	if !ok || !now.Before(entry.expiresAt) {
		return models.Session{}, false
	}
	return entry.record, true
}

func (c *authReadCache) setSession(db *gorm.DB, gen uint64, key string, record models.Session, now time.Time) {
	if db == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != db || c.gen != gen {
		return
	}
	if c.sessions == nil {
		c.sessions = make(map[string]cachedSessionEntry)
	} else if len(c.sessions) >= authCacheMaxItems {
		for k, entry := range c.sessions {
			if !now.Before(entry.expiresAt) {
				delete(c.sessions, k)
			}
		}
		if len(c.sessions) >= authCacheMaxItems {
			c.sessions = make(map[string]cachedSessionEntry)
		}
	}
	c.sessions[key] = cachedSessionEntry{
		record:    record,
		expiresAt: now.Add(authCacheTTL),
	}
}

func (c *authReadCache) getUser(db *gorm.DB, uuid string, now time.Time) (models.User, bool) {
	if db == nil || uuid == "" {
		return models.User{}, false
	}
	c.mu.RLock()
	if c.db != db || c.users == nil {
		c.mu.RUnlock()
		return models.User{}, false
	}
	entry, ok := c.users[uuid]
	c.mu.RUnlock()
	if !ok || !now.Before(entry.expiresAt) {
		return models.User{}, false
	}
	return entry.user, true
}

func (c *authReadCache) setUser(db *gorm.DB, gen uint64, uuid string, user models.User, now time.Time) {
	if db == nil || uuid == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != db || c.gen != gen {
		return
	}
	if c.users == nil {
		c.users = make(map[string]cachedUserEntry)
	} else if len(c.users) >= authCacheMaxItems {
		for k, entry := range c.users {
			if !now.Before(entry.expiresAt) {
				delete(c.users, k)
			}
		}
		if len(c.users) >= authCacheMaxItems {
			c.users = make(map[string]cachedUserEntry)
		}
	}
	c.users[uuid] = cachedUserEntry{
		user:      user,
		expiresAt: now.Add(authCacheTTL),
	}
}

func invalidateSessionCache(plain string) {
	if plain == "" {
		return
	}
	key := SessionLookupKey(plain)
	authCache.mu.Lock()
	authCache.gen++
	if authCache.sessions != nil {
		delete(authCache.sessions, key)
		delete(authCache.sessions, plain)
	}
	authCache.mu.Unlock()
}

func invalidateAllSessionCache() {
	authCache.mu.Lock()
	authCache.gen++
	authCache.sessions = nil
	authCache.mu.Unlock()
}

// InvalidateUserCache drops cached user profile state for a single user.
func InvalidateUserCache(uuid string) {
	if uuid == "" {
		return
	}
	authCache.mu.Lock()
	authCache.gen++
	if authCache.users != nil {
		delete(authCache.users, uuid)
	}
	authCache.mu.Unlock()
}

func invalidateAllAuthCache() {
	authCache.mu.Lock()
	authCache.gen++
	authCache.sessions = nil
	authCache.users = nil
	authCache.mu.Unlock()
}
