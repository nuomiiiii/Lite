package metric

import (
	"container/list"
	"database/sql"
	"fmt"
)

const defaultSQLiteAxisCacheBytes int64 = 8 << 20

const (
	sqliteAxisKindPoint  byte = 1
	sqliteAxisKindRollup byte = 2
)

type sqliteAxisCacheKey struct {
	kind     byte
	id       int64
	codec    int
	checksum uint32
	count    int
}

type sqliteAxisCacheEntry struct {
	key        sqliteAxisCacheKey
	timestamps []int64
	records    []sqliteV4RollupRecord
	bytes      int64
}

type sqliteAxisCache struct {
	maxBytes int64
	used     int64
	order    *list.List
	items    map[sqliteAxisCacheKey]*list.Element
}

func newSQLiteAxisCache(maxBytes int64) *sqliteAxisCache {
	if maxBytes <= 0 {
		return nil
	}
	return &sqliteAxisCache{
		maxBytes: maxBytes,
		order:    list.New(),
		items:    make(map[sqliteAxisCacheKey]*list.Element),
	}
}

func (c *sqliteAxisCache) point(key sqliteAxisCacheKey) ([]int64, bool) {
	if c == nil {
		return nil, false
	}
	element, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*sqliteAxisCacheEntry).timestamps, true
}

func (c *sqliteAxisCache) rollup(key sqliteAxisCacheKey) ([]sqliteV4RollupRecord, bool) {
	if c == nil {
		return nil, false
	}
	element, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	records := element.Value.(*sqliteAxisCacheEntry).records
	return append([]sqliteV4RollupRecord(nil), records...), true
}

// rollupView returns the immutable cached axis without copying it. Dashboard
// summary decoding reads these fields into a local record and never mutates the
// shared slice.
func (c *sqliteAxisCache) rollupView(key sqliteAxisCacheKey) ([]sqliteV4RollupRecord, bool) {
	if c == nil {
		return nil, false
	}
	element, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*sqliteAxisCacheEntry).records, true
}

func (c *sqliteAxisCache) add(entry *sqliteAxisCacheEntry) {
	if c == nil || entry == nil || entry.bytes <= 0 || entry.bytes > c.maxBytes {
		return
	}
	if existing := c.items[entry.key]; existing != nil {
		c.order.MoveToFront(existing)
		return
	}
	element := c.order.PushFront(entry)
	c.items[entry.key] = element
	c.used += entry.bytes
	for c.used > c.maxBytes {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		oldEntry := oldest.Value.(*sqliteAxisCacheEntry)
		delete(c.items, oldEntry.key)
		c.used -= oldEntry.bytes
		c.order.Remove(oldest)
	}
}

func (s *Store) decodeSQLitePointBlockCached(codec, count int, checksum uint32, payload []byte,
	axisID, axisCodec, axisChecksum sql.NullInt64, axisPayload []byte,
) ([]sqliteV4BlockPoint, error) {
	if codec != sqliteV6SharedPointBlockCodec || !axisID.Valid || axisID.Int64 <= 0 {
		return decodeSQLiteStoredPointBlock(codec, count, checksum, payload, int(axisCodec.Int64), uint32(axisChecksum.Int64), axisPayload)
	}
	key := sqliteAxisCacheKey{
		kind: sqliteAxisKindPoint, id: axisID.Int64, codec: int(axisCodec.Int64),
		checksum: uint32(axisChecksum.Int64), count: count,
	}
	s.axisCacheMu.Lock()
	timestamps, ok := s.axisCache.point(key)
	s.axisCacheMu.Unlock()
	if !ok {
		var err error
		timestamps, err = decodeSQLiteV6PointAxis(key.codec, count, key.checksum, axisPayload)
		if err != nil {
			return nil, err
		}
		s.axisCacheMu.Lock()
		s.axisCache.add(&sqliteAxisCacheEntry{key: key, timestamps: timestamps, bytes: int64(len(timestamps))*8 + 96})
		s.axisCacheMu.Unlock()
	}
	return decodeSQLiteV6PointValues(count, checksum, payload, timestamps)
}

func (s *Store) decodeSQLiteRollupBlockCached(codec, count int, checksum uint32, payload []byte,
	axisID, axisCodec, axisChecksum sql.NullInt64, axisPayload []byte,
	digestCodec int, digestChecksum uint32, digestPayload []byte, needDigest bool,
) ([]sqliteV4RollupRecord, error) {
	if codec != sqliteV4SharedRollupBlockCodec || !axisID.Valid || axisID.Int64 <= 0 {
		return decodeSQLiteV4RollupBlockWithAxisReference(codec, count, checksum, payload,
			axisCodec, axisChecksum, axisPayload, digestCodec, digestChecksum, digestPayload, needDigest)
	}
	key := sqliteAxisCacheKey{
		kind: sqliteAxisKindRollup, id: axisID.Int64, codec: int(axisCodec.Int64),
		checksum: uint32(axisChecksum.Int64), count: count,
	}
	s.axisCacheMu.Lock()
	records, ok := s.axisCache.rollup(key)
	s.axisCacheMu.Unlock()
	if !ok {
		var err error
		records, err = decodeSQLiteV4RollupAxis(key.codec, count, key.checksum, axisPayload)
		if err != nil {
			return nil, err
		}
		cached := append([]sqliteV4RollupRecord(nil), records...)
		s.axisCacheMu.Lock()
		s.axisCache.add(&sqliteAxisCacheEntry{key: key, records: cached, bytes: int64(len(cached))*112 + 96})
		s.axisCacheMu.Unlock()
	}
	records, err := decodeSQLiteV4SharedRollupValues(count, checksum, payload, records)
	if err != nil {
		return nil, err
	}
	if needDigest {
		if digestCodec == sqliteV4StructuredRollupDigestCodec || digestCodec == sqliteV4LegacyStructuredDigestCodec {
			err = decodeSQLiteV4StructuredRollupDigests(records, digestCodec, digestChecksum, digestPayload)
		} else {
			err = decodeSQLiteV4RollupDigestSection(records, digestCodec, digestChecksum, digestPayload)
		}
		if err != nil {
			return nil, fmt.Errorf("metric: decode cached SQLite rollup digest: %w", err)
		}
	}
	return records, nil
}
