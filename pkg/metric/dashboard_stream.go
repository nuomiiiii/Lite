package metric

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
)

type sqliteDashboardPointVisitor func(timestamp int64, valueBits uint64) error
type sqliteDashboardRollupVisitor func(record sqliteV4RollupRecord) error

type dashboardAxisQueryCache struct {
	points  map[sqliteAxisCacheKey][]int64
	rollups map[sqliteAxisCacheKey][]sqliteV4RollupRecord
}

type dashboardAxisQueryCacheKey struct{}

func withDashboardAxisQueryCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(dashboardAxisQueryCacheKey{}).(*dashboardAxisQueryCache); ok {
		return ctx
	}
	return context.WithValue(ctx, dashboardAxisQueryCacheKey{}, &dashboardAxisQueryCache{})
}

func dashboardQueryCache(ctx context.Context) *dashboardAxisQueryCache {
	cache, _ := ctx.Value(dashboardAxisQueryCacheKey{}).(*dashboardAxisQueryCache)
	return cache
}

func (s *Store) visitSQLiteDashboardPointBlock(
	ctx context.Context,
	codec, count int,
	checksum uint32,
	payload []byte,
	axisID, axisCodec, axisChecksum sql.NullInt64,
	axisPayload []byte,
	visit sqliteDashboardPointVisitor,
) (int64, int64, error) {
	if codec == sqliteV4BlockCodec {
		return visitSQLiteV4DashboardPointBlock(codec, count, checksum, payload, visit)
	}
	if codec != sqliteV6SharedPointBlockCodec {
		return 0, 0, fmt.Errorf("metric: unsupported SQLite point block codec %d", codec)
	}
	timestamps, err := s.dashboardPointAxis(ctx, count, axisID, axisCodec, axisChecksum, axisPayload)
	if err != nil {
		return 0, 0, err
	}
	if err := visitSQLiteV6DashboardPointValues(count, checksum, payload, timestamps, visit); err != nil {
		return 0, 0, err
	}
	return timestamps[0], timestamps[len(timestamps)-1], nil
}

func (s *Store) dashboardPointAxis(ctx context.Context, count int, axisID, axisCodec, axisChecksum sql.NullInt64, axisPayload []byte) ([]int64, error) {
	if !axisID.Valid || axisID.Int64 <= 0 {
		return decodeSQLiteV6PointAxis(int(axisCodec.Int64), count, uint32(axisChecksum.Int64), axisPayload)
	}
	key := sqliteAxisCacheKey{
		kind: sqliteAxisKindPoint, id: axisID.Int64, codec: int(axisCodec.Int64),
		checksum: uint32(axisChecksum.Int64), count: count,
	}
	cache := dashboardQueryCache(ctx)
	if cache != nil && cache.points != nil {
		if timestamps, ok := cache.points[key]; ok {
			return timestamps, nil
		}
	}
	timestamps, err := decodeSQLiteV6PointAxis(key.codec, count, key.checksum, axisPayload)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		if cache.points == nil {
			cache.points = make(map[sqliteAxisCacheKey][]int64)
		}
		cache.points[key] = timestamps
	}
	return timestamps, nil
}

func visitSQLiteV4DashboardPointBlock(codec, expectedCount int, expectedChecksum uint32, payload []byte, visit sqliteDashboardPointVisitor) (int64, int64, error) {
	if codec != sqliteV4BlockCodec {
		return 0, 0, fmt.Errorf("metric: unsupported SQLite V4 point block codec %d", codec)
	}
	if len(payload) < 2 {
		return 0, 0, fmt.Errorf("metric: truncated SQLite V4 point block")
	}
	if crc32.ChecksumIEEE(payload) != expectedChecksum {
		return 0, 0, fmt.Errorf("metric: SQLite V4 point block checksum mismatch")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return 0, 0, err
	}
	reader := bytes.NewReader(raw)
	if err := readSQLiteDashboardMagic(reader, sqliteV4BlockMagic); err != nil {
		return 0, 0, fmt.Errorf("metric: invalid SQLite V4 point block header")
	}
	count, err := readSQLiteDashboardCount(reader, expectedCount, sqliteV4MaxDecodedPoints, "SQLite V4 point")
	if err != nil {
		return 0, 0, err
	}
	timestamps, err := readSQLiteDashboardTimestamps(reader, count, "SQLite V4")
	if err != nil {
		return 0, 0, err
	}
	if err := visitSQLiteDashboardGorillaValues(reader, raw, timestamps, visit, "SQLite V4"); err != nil {
		return 0, 0, err
	}
	if err := skipSQLiteDashboardPointMetadata(reader, count, "SQLite V4"); err != nil {
		return 0, 0, err
	}
	return timestamps[0], timestamps[len(timestamps)-1], nil
}

func visitSQLiteV6DashboardPointValues(expectedCount int, expectedChecksum uint32, payload []byte, timestamps []int64, visit sqliteDashboardPointVisitor) error {
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return fmt.Errorf("metric: invalid SQLite V6 shared point values")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(raw)
	if err := readSQLiteDashboardMagic(reader, sqliteV6PointValueMagic); err != nil {
		return fmt.Errorf("metric: invalid SQLite V6 point value header")
	}
	count, err := readSQLiteDashboardCount(reader, expectedCount, sqliteV4MaxDecodedPoints, "SQLite V6 point value")
	if err != nil {
		return err
	}
	if len(timestamps) != count {
		return fmt.Errorf("metric: SQLite V6 point value count mismatch")
	}
	if err := visitSQLiteDashboardGorillaValues(reader, raw, timestamps, visit, "SQLite V6"); err != nil {
		return err
	}
	return skipSQLiteDashboardPointMetadata(reader, count, "SQLite V6")
}

func visitSQLiteDashboardGorillaValues(reader *bytes.Reader, raw []byte, timestamps []int64, visit sqliteDashboardPointVisitor, format string) error {
	valueBitCount, err := binary.ReadUvarint(reader)
	if err != nil || valueBitCount < 64 || valueBitCount > uint64(reader.Len())*8 {
		return fmt.Errorf("metric: invalid %s value bit stream", format)
	}
	valueBytes, err := sqliteDashboardReaderSlice(reader, raw, int((valueBitCount+7)/8))
	if err != nil {
		return fmt.Errorf("metric: read %s value bit stream: %w", format, err)
	}
	bitReader := newSQLiteV4BitReader(valueBytes, int(valueBitCount))
	previousBits, err := bitReader.readBits(64)
	if err != nil {
		return err
	}
	if err := visit(timestamps[0], previousBits); err != nil {
		return err
	}
	previousLeading, previousTrailing := 0, 0
	windowValid := false
	for index := 1; index < len(timestamps); index++ {
		changed, err := bitReader.readBit()
		if err != nil {
			return err
		}
		if changed {
			newWindow, err := bitReader.readBit()
			if err != nil {
				return err
			}
			leading, trailing := previousLeading, previousTrailing
			if newWindow {
				leadingBits, err := bitReader.readBits(6)
				if err != nil {
					return err
				}
				significantBits, err := bitReader.readBits(6)
				if err != nil {
					return err
				}
				significant := int(significantBits)
				if significant == 0 {
					significant = 64
				}
				leading = int(leadingBits)
				trailing = 64 - leading - significant
				if trailing < 0 {
					return fmt.Errorf("metric: invalid %s Gorilla window", format)
				}
				previousLeading, previousTrailing = leading, trailing
				windowValid = true
			} else if !windowValid {
				return fmt.Errorf("metric: %s Gorilla stream reused a missing window", format)
			}
			xorBits, err := bitReader.readBits(64 - leading - trailing)
			if err != nil {
				return err
			}
			previousBits ^= xorBits << trailing
		}
		if err := visit(timestamps[index], previousBits); err != nil {
			return err
		}
	}
	return nil
}

func skipSQLiteDashboardPointMetadata(reader *bytes.Reader, count int, format string) error {
	labelCount, err := binary.ReadUvarint(reader)
	if err != nil || labelCount == 0 || labelCount > uint64(count) {
		return fmt.Errorf("metric: invalid %s label dictionary", format)
	}
	for index := uint64(0); index < labelCount; index++ {
		length, err := binary.ReadUvarint(reader)
		if err != nil || length > uint64(reader.Len()) {
			return fmt.Errorf("metric: invalid %s label length", format)
		}
		if _, err := reader.Seek(int64(length), io.SeekCurrent); err != nil {
			return err
		}
	}
	for index := 0; index < count; index++ {
		labelIndex, err := binary.ReadUvarint(reader)
		if err != nil || labelIndex >= labelCount {
			return fmt.Errorf("metric: invalid %s label index", format)
		}
	}
	createdAt, err := binary.ReadVarint(reader)
	if err != nil {
		return fmt.Errorf("metric: decode %s creation time: %w", format, err)
	}
	for index := 1; index < count; index++ {
		delta, err := binary.ReadVarint(reader)
		if err != nil {
			return fmt.Errorf("metric: decode %s creation-time delta: %w", format, err)
		}
		createdAt, err = checkedAddInt64(createdAt, delta)
		if err != nil {
			return err
		}
	}
	if reader.Len() != 0 {
		return fmt.Errorf("metric: %s point block contains trailing data", format)
	}
	return nil
}

func (s *Store) visitSQLiteDashboardRollupBlock(
	ctx context.Context,
	codec, count int,
	checksum uint32,
	payload []byte,
	axisID, axisCodec, axisChecksum sql.NullInt64,
	axisPayload []byte,
	visit sqliteDashboardRollupVisitor,
) (int64, int64, error) {
	if codec != sqliteV4SharedRollupBlockCodec {
		records, err := s.decodeSQLiteRollupBlockCached(codec, count, checksum, payload,
			axisID, axisCodec, axisChecksum, axisPayload, 0, 0, nil, false)
		if err != nil {
			return 0, 0, err
		}
		for _, record := range records {
			if err := visit(record); err != nil {
				return 0, 0, err
			}
		}
		return records[0].bucketNano, records[len(records)-1].bucketNano, nil
	}
	axis, err := s.dashboardRollupAxis(ctx, count, axisID, axisCodec, axisChecksum, axisPayload)
	if err != nil {
		return 0, 0, err
	}
	if err := visitSQLiteV4SharedDashboardRollupValues(count, checksum, payload, axis, visit); err != nil {
		return 0, 0, err
	}
	return axis[0].bucketNano, axis[len(axis)-1].bucketNano, nil
}

func (s *Store) dashboardRollupAxis(ctx context.Context, count int, axisID, axisCodec, axisChecksum sql.NullInt64, axisPayload []byte) ([]sqliteV4RollupRecord, error) {
	if !axisID.Valid || axisID.Int64 <= 0 {
		return decodeSQLiteV4RollupAxis(int(axisCodec.Int64), count, uint32(axisChecksum.Int64), axisPayload)
	}
	key := sqliteAxisCacheKey{
		kind: sqliteAxisKindRollup, id: axisID.Int64, codec: int(axisCodec.Int64),
		checksum: uint32(axisChecksum.Int64), count: count,
	}
	cache := dashboardQueryCache(ctx)
	if cache != nil && cache.rollups != nil {
		if records, ok := cache.rollups[key]; ok {
			return records, nil
		}
	}
	records, err := decodeSQLiteV4RollupAxis(key.codec, count, key.checksum, axisPayload)
	if err != nil {
		return nil, err
	}
	if cache != nil {
		if cache.rollups == nil {
			cache.rollups = make(map[sqliteAxisCacheKey][]sqliteV4RollupRecord)
		}
		cache.rollups[key] = records
	}
	return records, nil
}

type sqliteDashboardLosslessIterator struct {
	count       int
	index       int
	exponent    int
	scale       float64
	integer     bool
	intReader   bytes.Reader
	current     int64
	floatReader *sqliteV4BitReader
	previous    uint64
}

func newSQLiteDashboardLosslessIterator(reader *bytes.Reader, raw []byte, count int) (sqliteDashboardLosslessIterator, error) {
	mode, err := reader.ReadByte()
	if err != nil {
		return sqliteDashboardLosslessIterator{}, err
	}
	iterator := sqliteDashboardLosslessIterator{count: count, scale: 1}
	if mode == sqliteV4ValueRaw {
		bitCount, err := binary.ReadUvarint(reader)
		if err != nil || bitCount < 64 || bitCount > uint64(reader.Len())*8 {
			return iterator, fmt.Errorf("metric: invalid SQLite V4 raw value stream")
		}
		encoded, err := sqliteDashboardReaderSlice(reader, raw, int((bitCount+7)/8))
		if err != nil {
			return iterator, err
		}
		iterator.floatReader = newSQLiteV4BitReader(encoded, int(bitCount))
		return iterator, nil
	}
	switch {
	case mode == sqliteV4ValueInteger:
	case mode >= 2 && mode <= 5:
		iterator.exponent = int(mode - 1)
	default:
		return iterator, fmt.Errorf("metric: unsupported SQLite V4 value mode %d", mode)
	}
	iterator.integer = true
	iterator.scale = math.Pow10(iterator.exponent)
	iterator.intReader = *reader
	current, err := binary.ReadVarint(reader)
	if err != nil {
		return iterator, err
	}
	for index := 1; index < count; index++ {
		delta, err := binary.ReadVarint(reader)
		if err != nil {
			return iterator, err
		}
		current, err = checkedAddInt64(current, delta)
		if err != nil {
			return iterator, err
		}
	}
	return iterator, nil
}

func (iterator *sqliteDashboardLosslessIterator) next() (uint64, error) {
	if iterator.index >= iterator.count {
		return 0, io.EOF
	}
	if iterator.integer {
		if iterator.index == 0 {
			current, err := binary.ReadVarint(&iterator.intReader)
			if err != nil {
				return 0, err
			}
			iterator.current = current
		} else {
			delta, err := binary.ReadVarint(&iterator.intReader)
			if err != nil {
				return 0, err
			}
			iterator.current, err = checkedAddInt64(iterator.current, delta)
			if err != nil {
				return 0, err
			}
		}
		iterator.index++
		return math.Float64bits(float64(iterator.current) / iterator.scale), nil
	}
	if iterator.index == 0 {
		value, err := iterator.floatReader.readBits(64)
		if err != nil {
			return 0, err
		}
		iterator.previous = value
		iterator.index++
		return value, nil
	}
	changed, err := iterator.floatReader.readBit()
	if err != nil {
		return 0, err
	}
	if changed {
		leading, err := iterator.floatReader.readBits(6)
		if err != nil {
			return 0, err
		}
		significantBits, err := iterator.floatReader.readBits(6)
		if err != nil {
			return 0, err
		}
		significant := int(significantBits)
		if significant == 0 {
			significant = 64
		}
		trailing := 64 - int(leading) - significant
		if trailing < 0 {
			return 0, fmt.Errorf("metric: invalid SQLite V4 rollup float window")
		}
		xorBits, err := iterator.floatReader.readBits(significant)
		if err != nil {
			return 0, err
		}
		iterator.previous ^= xorBits << trailing
	}
	iterator.index++
	return iterator.previous, nil
}

func visitSQLiteV4SharedDashboardRollupValues(expectedCount int, expectedChecksum uint32, payload []byte, axis []sqliteV4RollupRecord, visit sqliteDashboardRollupVisitor) error {
	if len(axis) != expectedCount {
		return fmt.Errorf("metric: SQLite V4 shared rollup axis count mismatch")
	}
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return fmt.Errorf("metric: SQLite V4 shared rollup value checksum mismatch")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(raw)
	if err := readSQLiteDashboardMagic(reader, sqliteV4RollupValueMagic); err != nil {
		return fmt.Errorf("metric: invalid SQLite V4 shared rollup value header")
	}
	count, err := readSQLiteDashboardCount(reader, expectedCount, sqliteV4MaxDecodedRollupRows, "SQLite V4 shared rollup value")
	if err != nil {
		return err
	}
	var iterators [sqliteV4RollupFloatFieldCount]sqliteDashboardLosslessIterator
	for index := range iterators {
		iterators[index], err = newSQLiteDashboardLosslessIterator(reader, raw, count)
		if err != nil {
			return err
		}
	}
	if reader.Len() != 0 {
		return fmt.Errorf("metric: SQLite V4 shared rollup values contain trailing data")
	}
	for index := 0; index < count; index++ {
		record := axis[index]
		targets := [...]*uint64{&record.sumBits, &record.sumSqBits, &record.minBits, &record.maxBits, &record.firstBits, &record.lastBits}
		for field := range iterators {
			value, err := iterators[field].next()
			if err != nil {
				return err
			}
			*targets[field] = value
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return nil
}

func readSQLiteDashboardMagic(reader *bytes.Reader, want string) error {
	var magic [4]byte
	if len(want) != len(magic) {
		return fmt.Errorf("metric: unsupported dashboard block magic length")
	}
	if _, err := io.ReadFull(reader, magic[:]); err != nil || string(magic[:]) != want {
		return fmt.Errorf("metric: invalid dashboard block header")
	}
	return nil
}

func readSQLiteDashboardCount(reader *bytes.Reader, expected, maximum int, name string) (int, error) {
	count64, err := binary.ReadUvarint(reader)
	if err != nil || count64 == 0 || count64 > uint64(maximum) {
		return 0, fmt.Errorf("metric: invalid %s count", name)
	}
	count := int(count64)
	if expected >= 0 && count != expected {
		return 0, fmt.Errorf("metric: %s count mismatch: header=%d row=%d", name, count, expected)
	}
	return count, nil
}

func readSQLiteDashboardTimestamps(reader *bytes.Reader, count int, name string) ([]int64, error) {
	timestamps := make([]int64, count)
	first, err := binary.ReadVarint(reader)
	if err != nil {
		return nil, fmt.Errorf("metric: decode %s first timestamp: %w", name, err)
	}
	timestamps[0] = first
	if count == 1 {
		return timestamps, nil
	}
	previousDelta, err := binary.ReadVarint(reader)
	if err != nil || previousDelta <= 0 {
		return nil, fmt.Errorf("metric: invalid %s timestamp delta", name)
	}
	timestamps[1], err = checkedAddInt64(first, previousDelta)
	if err != nil {
		return nil, err
	}
	for index := 2; index < count; index++ {
		deltaOfDelta, err := binary.ReadVarint(reader)
		if err != nil {
			return nil, fmt.Errorf("metric: decode %s timestamp delta-of-delta: %w", name, err)
		}
		delta, ok := checkedAddInt64Value(previousDelta, deltaOfDelta)
		if !ok || delta <= 0 {
			return nil, fmt.Errorf("metric: invalid %s timestamp delta", name)
		}
		timestamps[index], err = checkedAddInt64(timestamps[index-1], delta)
		if err != nil {
			return nil, err
		}
		previousDelta = delta
	}
	return timestamps, nil
}

func sqliteDashboardReaderSlice(reader *bytes.Reader, raw []byte, length int) ([]byte, error) {
	if length < 0 || length > reader.Len() {
		return nil, io.ErrUnexpectedEOF
	}
	offset := int(reader.Size()) - reader.Len()
	if _, err := reader.Seek(int64(length), io.SeekCurrent); err != nil {
		return nil, err
	}
	return raw[offset : offset+length], nil
}
