package clients

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

// clientListCacheTTL covers the admin server list refresh. A repeat open or
// that refresh reuses the last read. Edits drop the cache immediately.
//
// clientListCacheTTL 盖住后台服务器列表的自动刷新。再次打开或这次刷新沿用
// 上一次的结果。编辑会立刻丢掉缓存。
const (
	clientListCacheTTL       = 20 * time.Second
	clientTokenCacheTTL      = 5 * time.Minute
	clientTokenCacheMaxItems = 4096
)

type clientBasicInfoCache struct {
	mu       sync.Mutex
	db       *gorm.DB
	gen      uint64
	storedAt time.Time
	clients  []models.Client
	valid    bool
}

func (c *clientBasicInfoCache) generation(db *gorm.DB) uint64 {
	c.mu.Lock()
	if c.db != db {
		c.gen++
		c.db = db
		c.valid = false
		c.clients = nil
	}
	gen := c.gen
	c.mu.Unlock()
	return gen
}

func (c *clientBasicInfoCache) get(db *gorm.DB, now time.Time, ttl time.Duration) ([]models.Client, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid || c.db != db || now.Sub(c.storedAt) >= ttl {
		return nil, false
	}
	return cloneClientList(c.clients), true
}

func (c *clientBasicInfoCache) set(db *gorm.DB, gen uint64, now time.Time, list []models.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db != db || c.gen != gen {
		return
	}
	c.storedAt = now
	c.clients = cloneClientList(list)
	c.valid = true
}

func (c *clientBasicInfoCache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.valid = false
	c.clients = nil
	c.mu.Unlock()
}

type clientTokenCacheEntry struct {
	uuid      string
	expiresAt time.Time
}

type clientTokenUUIDCache struct {
	mu     sync.RWMutex
	db     *gorm.DB
	gen    uint64
	tokens map[string]clientTokenCacheEntry
}

func (c *clientTokenUUIDCache) generation(db *gorm.DB) uint64 {
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
		c.tokens = nil
	}
	gen := c.gen
	c.mu.Unlock()
	return gen
}

func (c *clientTokenUUIDCache) get(db *gorm.DB, token string, now time.Time) (string, bool) {
	if db == nil || token == "" {
		return "", false
	}
	c.mu.RLock()
	if c.db != db || c.tokens == nil {
		c.mu.RUnlock()
		return "", false
	}
	entry, ok := c.tokens[token]
	c.mu.RUnlock()
	if !ok || !now.Before(entry.expiresAt) {
		return "", false
	}
	return entry.uuid, true
}

func (c *clientTokenUUIDCache) set(db *gorm.DB, gen uint64, token, uuid string, now time.Time) {
	if db == nil || token == "" || uuid == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Refuse to insert if the DB changed or any client mutation invalidated the
	// cache while the caller's SELECT was in flight (closes TOCTOU race).
	if c.db != db || c.gen != gen {
		return
	}
	if c.tokens == nil {
		c.tokens = make(map[string]clientTokenCacheEntry)
	} else if len(c.tokens) >= clientTokenCacheMaxItems {
		for k, entry := range c.tokens {
			if !now.Before(entry.expiresAt) {
				delete(c.tokens, k)
			}
		}
		if len(c.tokens) >= clientTokenCacheMaxItems {
			c.tokens = make(map[string]clientTokenCacheEntry)
		}
	}
	c.tokens[token] = clientTokenCacheEntry{
		uuid:      uuid,
		expiresAt: now.Add(clientTokenCacheTTL),
	}
}

func (c *clientTokenUUIDCache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.tokens = nil
	c.mu.Unlock()
}

var (
	basicInfoCache clientBasicInfoCache
	tokenUUIDCache clientTokenUUIDCache
	clientCacheSeq atomic.Uint64
)

func invalidateClientListCache() {
	clientCacheSeq.Add(1)
	basicInfoCache.invalidate()
	tokenUUIDCache.invalidate()
}

// CacheGeneration returns a monotonic counter bumped on every client mutation.
func CacheGeneration() uint64 {
	return clientCacheSeq.Load()
}

func cloneClientList(in []models.Client) []models.Client {
	out := make([]models.Client, len(in))
	for index := range in {
		out[index] = in[index]
		if in[index].TrafficResetDay != nil {
			day := *in[index].TrafficResetDay
			out[index].TrafficResetDay = &day
		}
		if in[index].ExpiredAt != nil {
			stamp := *in[index].ExpiredAt
			out[index].ExpiredAt = &stamp
		}
		if in[index].PreviousTokenExpiresAt != nil {
			stamp := *in[index].PreviousTokenExpiresAt
			out[index].PreviousTokenExpiresAt = &stamp
		}
	}
	return out
}
