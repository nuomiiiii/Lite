package metric

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/bits"
	"sort"
)

const (
	sqliteV6SharedPointBlockCodec = 2
	sqliteV6PointAxisCodec        = 1
	sqliteV6PointAxisMagic        = "KPA6"
	sqliteV6PointValueMagic       = "KPV6"
)

func encodeSQLiteV6SharedPointBlock(points []sqliteV4BlockPoint) (sqliteV4EncodedBlock, error) {
	if len(points) == 0 {
		return sqliteV4EncodedBlock{}, fmt.Errorf("metric: cannot encode an empty SQLite V6 point block")
	}
	if len(points) > sqliteV4MaxDecodedPoints {
		return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V6 point block is too large: %d", len(points))
	}
	points = append([]sqliteV4BlockPoint(nil), points...)
	sort.SliceStable(points, func(i, j int) bool { return points[i].timestamp < points[j].timestamp })
	for i := 1; i < len(points); i++ {
		if points[i].timestamp <= points[i-1].timestamp {
			return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V6 point timestamps must be strictly increasing")
		}
	}

	axisPayload, err := encodeSQLiteV6PointAxis(points)
	if err != nil {
		return sqliteV4EncodedBlock{}, err
	}
	valuePayload, err := encodeSQLiteV6PointValues(points)
	if err != nil {
		return sqliteV4EncodedBlock{}, err
	}
	return sqliteV4EncodedBlock{
		startNano:    points[0].timestamp,
		endNano:      points[len(points)-1].timestamp,
		count:        len(points),
		codec:        sqliteV6SharedPointBlockCodec,
		checksum:     crc32.ChecksumIEEE(valuePayload),
		payload:      valuePayload,
		axisCodec:    sqliteV6PointAxisCodec,
		axisChecksum: crc32.ChecksumIEEE(axisPayload),
		axisPayload:  axisPayload,
	}, nil
}

func encodeSQLiteV6PointAxis(points []sqliteV4BlockPoint) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString(sqliteV6PointAxisMagic)
	appendUvarintTo(&raw, uint64(len(points)))
	appendVarintTo(&raw, points[0].timestamp)
	if len(points) > 1 {
		delta, ok := checkedSubInt64(points[1].timestamp, points[0].timestamp)
		if !ok || delta <= 0 {
			return nil, fmt.Errorf("metric: invalid SQLite V6 timestamp delta")
		}
		appendVarintTo(&raw, delta)
		previousDelta := delta
		for i := 2; i < len(points); i++ {
			delta, ok = checkedSubInt64(points[i].timestamp, points[i-1].timestamp)
			if !ok || delta <= 0 {
				return nil, fmt.Errorf("metric: invalid SQLite V6 timestamp delta")
			}
			deltaOfDelta, ok := checkedSubInt64(delta, previousDelta)
			if !ok {
				return nil, fmt.Errorf("metric: SQLite V6 timestamp delta-of-delta overflow")
			}
			appendVarintTo(&raw, deltaOfDelta)
			previousDelta = delta
		}
	}
	return compressSQLiteV6PointSection(raw.Bytes())
}

func decodeSQLiteV6PointAxis(codec, expectedCount int, expectedChecksum uint32, payload []byte) ([]int64, error) {
	if codec != sqliteV6PointAxisCodec || len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return nil, fmt.Errorf("metric: invalid SQLite V6 shared point axis")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(raw)
	magic := make([]byte, len(sqliteV6PointAxisMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != sqliteV6PointAxisMagic {
		return nil, fmt.Errorf("metric: invalid SQLite V6 point axis header")
	}
	count64, err := binary.ReadUvarint(reader)
	if err != nil || count64 == 0 || count64 > sqliteV4MaxDecodedPoints {
		return nil, fmt.Errorf("metric: invalid SQLite V6 point axis count")
	}
	count := int(count64)
	if expectedCount >= 0 && count != expectedCount {
		return nil, fmt.Errorf("metric: SQLite V6 point axis count mismatch: header=%d row=%d", count, expectedCount)
	}
	timestamps := make([]int64, count)
	timestamps[0], err = binary.ReadVarint(reader)
	if err != nil {
		return nil, fmt.Errorf("metric: decode SQLite V6 first timestamp: %w", err)
	}
	if count > 1 {
		previousDelta, err := binary.ReadVarint(reader)
		if err != nil || previousDelta <= 0 {
			return nil, fmt.Errorf("metric: invalid SQLite V6 timestamp delta")
		}
		timestamps[1], err = checkedAddInt64(timestamps[0], previousDelta)
		if err != nil {
			return nil, err
		}
		for i := 2; i < count; i++ {
			deltaOfDelta, err := binary.ReadVarint(reader)
			if err != nil {
				return nil, fmt.Errorf("metric: decode SQLite V6 timestamp delta-of-delta: %w", err)
			}
			delta, ok := checkedAddInt64Value(previousDelta, deltaOfDelta)
			if !ok || delta <= 0 {
				return nil, fmt.Errorf("metric: invalid SQLite V6 timestamp delta")
			}
			timestamps[i], err = checkedAddInt64(timestamps[i-1], delta)
			if err != nil {
				return nil, err
			}
			previousDelta = delta
		}
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("metric: SQLite V6 point axis contains trailing data")
	}
	return timestamps, nil
}

func encodeSQLiteV6PointValues(points []sqliteV4BlockPoint) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString(sqliteV6PointValueMagic)
	appendUvarintTo(&raw, uint64(len(points)))

	valueWriter := newSQLiteV4BitWriter()
	valueWriter.writeBits(points[0].valueBits, 64)
	previousBits := points[0].valueBits
	previousLeading, previousTrailing := 0, 0
	windowValid := false
	for i := 1; i < len(points); i++ {
		xor := previousBits ^ points[i].valueBits
		if xor == 0 {
			valueWriter.writeBit(false)
			previousBits = points[i].valueBits
			continue
		}
		valueWriter.writeBit(true)
		leading := bits.LeadingZeros64(xor)
		trailing := bits.TrailingZeros64(xor)
		if windowValid && leading >= previousLeading && trailing >= previousTrailing {
			valueWriter.writeBit(false)
			valueWriter.writeBits(xor>>previousTrailing, 64-previousLeading-previousTrailing)
		} else {
			valueWriter.writeBit(true)
			significant := 64 - leading - trailing
			valueWriter.writeBits(uint64(leading), 6)
			encodedSignificant := significant
			if significant == 64 {
				encodedSignificant = 0
			}
			valueWriter.writeBits(uint64(encodedSignificant), 6)
			valueWriter.writeBits(xor>>trailing, significant)
			previousLeading, previousTrailing = leading, trailing
			windowValid = true
		}
		previousBits = points[i].valueBits
	}
	valueBytes, valueBits := valueWriter.bytes()
	appendUvarintTo(&raw, uint64(valueBits))
	raw.Write(valueBytes)

	labelIndex := make(map[string]uint64)
	labels := make([]string, 0, 1)
	for _, point := range points {
		if _, ok := labelIndex[point.labels]; ok {
			continue
		}
		labelIndex[point.labels] = uint64(len(labels))
		labels = append(labels, point.labels)
	}
	appendUvarintTo(&raw, uint64(len(labels)))
	for _, label := range labels {
		appendUvarintTo(&raw, uint64(len(label)))
		raw.WriteString(label)
	}
	for _, point := range points {
		appendUvarintTo(&raw, labelIndex[point.labels])
	}

	appendVarintTo(&raw, points[0].createdAt)
	for i := 1; i < len(points); i++ {
		delta, ok := checkedSubInt64(points[i].createdAt, points[i-1].createdAt)
		if !ok {
			return nil, fmt.Errorf("metric: SQLite V6 creation-time delta overflow")
		}
		appendVarintTo(&raw, delta)
	}
	return compressSQLiteV6PointSection(raw.Bytes())
}

func decodeSQLiteV6PointValues(expectedCount int, expectedChecksum uint32, payload []byte, timestamps []int64) ([]sqliteV4BlockPoint, error) {
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return nil, fmt.Errorf("metric: invalid SQLite V6 shared point values")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(raw)
	magic := make([]byte, len(sqliteV6PointValueMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != sqliteV6PointValueMagic {
		return nil, fmt.Errorf("metric: invalid SQLite V6 point value header")
	}
	count64, err := binary.ReadUvarint(reader)
	if err != nil || int(count64) != expectedCount || len(timestamps) != expectedCount {
		return nil, fmt.Errorf("metric: SQLite V6 point value count mismatch")
	}
	points := make([]sqliteV4BlockPoint, expectedCount)
	for i := range points {
		points[i].timestamp = timestamps[i]
	}

	valueBitCount, err := binary.ReadUvarint(reader)
	if err != nil || valueBitCount < 64 || valueBitCount > uint64(reader.Len())*8 {
		return nil, fmt.Errorf("metric: invalid SQLite V6 value bit stream")
	}
	valueByteCount := int((valueBitCount + 7) / 8)
	valueBytes := make([]byte, valueByteCount)
	if _, err := io.ReadFull(reader, valueBytes); err != nil {
		return nil, err
	}
	bitReader := newSQLiteV4BitReader(valueBytes, int(valueBitCount))
	firstValue, err := bitReader.readBits(64)
	if err != nil {
		return nil, err
	}
	points[0].valueBits = firstValue
	previousBits := firstValue
	previousLeading, previousTrailing := 0, 0
	windowValid := false
	for i := 1; i < len(points); i++ {
		changed, err := bitReader.readBit()
		if err != nil {
			return nil, err
		}
		if !changed {
			points[i].valueBits = previousBits
			continue
		}
		newWindow, err := bitReader.readBit()
		if err != nil {
			return nil, err
		}
		leading, trailing := previousLeading, previousTrailing
		if newWindow {
			leadingBits, err := bitReader.readBits(6)
			if err != nil {
				return nil, err
			}
			significantBits, err := bitReader.readBits(6)
			if err != nil {
				return nil, err
			}
			significant := int(significantBits)
			if significant == 0 {
				significant = 64
			}
			leading = int(leadingBits)
			trailing = 64 - leading - significant
			if trailing < 0 {
				return nil, fmt.Errorf("metric: invalid SQLite V6 Gorilla window")
			}
			previousLeading, previousTrailing = leading, trailing
			windowValid = true
		} else if !windowValid {
			return nil, fmt.Errorf("metric: SQLite V6 Gorilla stream reused a missing window")
		}
		xorBits, err := bitReader.readBits(64 - leading - trailing)
		if err != nil {
			return nil, err
		}
		previousBits ^= xorBits << trailing
		points[i].valueBits = previousBits
	}

	labelCount64, err := binary.ReadUvarint(reader)
	if err != nil || labelCount64 == 0 || labelCount64 > uint64(len(points)) {
		return nil, fmt.Errorf("metric: invalid SQLite V6 label dictionary")
	}
	labels := make([]string, int(labelCount64))
	for i := range labels {
		length, err := binary.ReadUvarint(reader)
		if err != nil || length > uint64(reader.Len()) {
			return nil, fmt.Errorf("metric: invalid SQLite V6 label length")
		}
		label := make([]byte, int(length))
		if _, err := io.ReadFull(reader, label); err != nil {
			return nil, err
		}
		labels[i] = string(label)
	}
	for i := range points {
		index, err := binary.ReadUvarint(reader)
		if err != nil || index >= uint64(len(labels)) {
			return nil, fmt.Errorf("metric: invalid SQLite V6 label index")
		}
		points[i].labels = labels[index]
	}

	points[0].createdAt, err = binary.ReadVarint(reader)
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(points); i++ {
		delta, err := binary.ReadVarint(reader)
		if err != nil {
			return nil, err
		}
		points[i].createdAt, err = checkedAddInt64(points[i-1].createdAt, delta)
		if err != nil {
			return nil, err
		}
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("metric: SQLite V6 point values contain trailing data")
	}
	return points, nil
}

func decodeSQLiteStoredPointBlock(codec, count int, checksum uint32, payload []byte, axisCodec int, axisChecksum uint32, axisPayload []byte) ([]sqliteV4BlockPoint, error) {
	if codec == sqliteV4BlockCodec {
		return decodeSQLiteV4Block(codec, count, checksum, payload)
	}
	if codec != sqliteV6SharedPointBlockCodec {
		return nil, fmt.Errorf("metric: unsupported SQLite point block codec %d", codec)
	}
	timestamps, err := decodeSQLiteV6PointAxis(axisCodec, count, axisChecksum, axisPayload)
	if err != nil {
		return nil, err
	}
	return decodeSQLiteV6PointValues(count, checksum, payload, timestamps)
}

func compressSQLiteV6PointSection(raw []byte) ([]byte, error) {
	payload := append([]byte{sqliteV4PayloadRaw}, raw...)
	var compressed bytes.Buffer
	compressed.WriteByte(sqliteV4PayloadDeflate)
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() < len(payload) {
		return append([]byte(nil), compressed.Bytes()...), nil
	}
	return payload, nil
}

func (s *Store) createSQLiteV6PointAxes(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY,
		hash BLOB NOT NULL UNIQUE,
		codec INTEGER NOT NULL,
		checksum INTEGER NOT NULL,
		point_count INTEGER NOT NULL,
		payload BLOB NOT NULL,
		CHECK(point_count > 0)
	)`, s.tables.pointAxes)); err != nil {
		return fmt.Errorf("metric: create SQLite V6 point axis table: %w", err)
	}
	return nil
}

func (s *Store) createSQLiteV6PointAxisReferences(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_point_blocks_axis_idx ON %s (axis_id)`, s.cfg.TablePrefix, s.tables.pointBlocks),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s_point_axis_gc
			AFTER DELETE ON %s
			WHEN OLD.axis_id IS NOT NULL
			BEGIN
				DELETE FROM %s WHERE id = OLD.axis_id
				  AND NOT EXISTS (SELECT 1 FROM %s WHERE axis_id = OLD.axis_id);
			END`, s.cfg.TablePrefix, s.tables.pointBlocks, s.tables.pointAxes, s.tables.pointBlocks),
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metric: create SQLite V6 point axis reference: %w", err)
		}
	}
	return nil
}

func (s *Store) storeSQLiteV6PointAxisTx(ctx context.Context, tx *sql.Tx, encoded sqliteV4EncodedBlock) (int64, error) {
	if encoded.axisCodec == 0 || len(encoded.axisPayload) == 0 {
		return 0, fmt.Errorf("metric: cannot store an empty SQLite V6 point axis")
	}
	hash := sha256.Sum256(encoded.axisPayload)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT OR IGNORE INTO %s (hash, codec, checksum, point_count, payload) VALUES (?, ?, ?, ?, ?)`, s.tables.pointAxes,
	), hash[:], encoded.axisCodec, int64(encoded.axisChecksum), encoded.count, encoded.axisPayload); err != nil {
		return 0, err
	}
	var id, checksum int64
	var codec, count int
	var payload []byte
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT id, codec, checksum, point_count, payload FROM %s WHERE hash = ?`, s.tables.pointAxes,
	), hash[:]).Scan(&id, &codec, &checksum, &count, &payload); err != nil {
		return 0, err
	}
	if codec != encoded.axisCodec || uint32(checksum) != encoded.axisChecksum || count != encoded.count || !bytes.Equal(payload, encoded.axisPayload) {
		return 0, fmt.Errorf("metric: SQLite V6 point axis hash collision")
	}
	return id, nil
}

func (s *Store) loadSQLiteV6PointAxis(ctx context.Context, q querier, axisID sql.NullInt64) (int, uint32, []byte, error) {
	if !axisID.Valid || axisID.Int64 <= 0 {
		return 0, 0, nil, nil
	}
	var codec int
	var checksum int64
	var payload []byte
	if err := q.QueryRowContext(ctx, fmt.Sprintf(`SELECT codec, checksum, payload FROM %s WHERE id = ?`, s.tables.pointAxes), axisID.Int64).Scan(&codec, &checksum, &payload); err != nil {
		return 0, 0, nil, err
	}
	return codec, uint32(checksum), payload, nil
}

func (s *Store) decodeSQLitePointBlockFromStorage(ctx context.Context, q querier, codec, count int, checksum uint32, payload []byte, axisID sql.NullInt64) ([]sqliteV4BlockPoint, error) {
	axisCodec, axisChecksum, axisPayload, err := s.loadSQLiteV6PointAxis(ctx, q, axisID)
	if err != nil {
		return nil, err
	}
	return decodeSQLiteStoredPointBlock(codec, count, checksum, payload, axisCodec, axisChecksum, axisPayload)
}

func (s *Store) migrateSQLiteV6SharedPointBlocks(ctx context.Context) (int64, int64, error) {
	var blockCount int64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE axis_id IS NULL OR codec != ?`, s.tables.pointBlocks,
	), sqliteV6SharedPointBlockCodec).Scan(&blockCount); err != nil {
		return 0, 0, err
	}
	if blockCount == 0 {
		return 0, 0, nil
	}
	s.reportMigrationProgress(MigrationPhaseSharingPointAxes, 0, blockCount, 0)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	type key struct{ seriesID, startNano int64 }
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, start_nano FROM %s WHERE axis_id IS NULL OR codec != ? ORDER BY series_id, start_nano`, s.tables.pointBlocks,
	), sqliteV6SharedPointBlockCodec)
	if err != nil {
		return 0, 0, err
	}
	keys := make([]key, 0, blockCount)
	for rows.Next() {
		var item key
		if err := rows.Scan(&item.seriesID, &item.startNano); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		keys = append(keys, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}

	var migratedBlocks, migratedPoints int64
	for _, item := range keys {
		var endNano, checksum int64
		var count, codec int
		var payload []byte
		var axisID sql.NullInt64
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT end_nano, point_count, codec, checksum, payload, axis_id FROM %s WHERE series_id = ? AND start_nano = ?`, s.tables.pointBlocks,
		), item.seriesID, item.startNano).Scan(&endNano, &count, &codec, &checksum, &payload, &axisID); err != nil {
			return migratedBlocks, migratedPoints, err
		}
		points, err := s.decodeSQLitePointBlockFromStorage(ctx, tx, codec, count, uint32(checksum), payload, axisID)
		if err != nil {
			return migratedBlocks, migratedPoints, fmt.Errorf("metric: decode point-axis migration series=%d start=%d: %w", item.seriesID, item.startNano, err)
		}
		if len(points) == 0 || points[0].timestamp != item.startNano || points[len(points)-1].timestamp != endNano {
			return migratedBlocks, migratedPoints, fmt.Errorf("metric: point-axis migration boundary mismatch series=%d start=%d", item.seriesID, item.startNano)
		}
		encoded, err := encodeSQLiteV6SharedPointBlock(points)
		if err != nil {
			return migratedBlocks, migratedPoints, err
		}
		decoded, err := decodeSQLiteStoredPointBlock(encoded.codec, encoded.count, encoded.checksum, encoded.payload, encoded.axisCodec, encoded.axisChecksum, encoded.axisPayload)
		if err != nil || !sqliteV4PointsEqual(points, decoded) {
			if err == nil {
				err = errors.New("point-axis round-trip validation changed data")
			}
			return migratedBlocks, migratedPoints, err
		}
		newAxisID, err := s.storeSQLiteV6PointAxisTx(ctx, tx, encoded)
		if err != nil {
			return migratedBlocks, migratedPoints, err
		}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET end_nano = ?, point_count = ?, codec = ?, checksum = ?, payload = ?, axis_id = ? WHERE series_id = ? AND start_nano = ?`, s.tables.pointBlocks,
		), encoded.endNano, encoded.count, encoded.codec, int64(encoded.checksum), encoded.payload, newAxisID, item.seriesID, item.startNano)
		if err != nil {
			return migratedBlocks, migratedPoints, err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err == nil {
				err = fmt.Errorf("metric: point-axis migration update affected %d rows", affected)
			}
			return migratedBlocks, migratedPoints, err
		}
		migratedBlocks++
		migratedPoints += int64(len(points))
		s.reportMigrationProgress(MigrationPhaseSharingPointAxes, migratedBlocks, blockCount, migratedPoints)
	}
	if err := tx.Commit(); err != nil {
		return migratedBlocks, migratedPoints, err
	}
	return migratedBlocks, migratedPoints, nil
}
