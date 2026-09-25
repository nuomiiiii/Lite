package metric

import (
	"bytes"
	"compress/flate"
	"fmt"
	"hash/crc32"
	"math/bits"
	"sort"
)

func encodeSQLiteV4Block(points []sqliteV4BlockPoint) (sqliteV4EncodedBlock, error) {
	if len(points) == 0 {
		return sqliteV4EncodedBlock{}, fmt.Errorf("metric: cannot encode an empty SQLite V4 point block")
	}
	if len(points) > sqliteV4MaxDecodedPoints {
		return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V4 point block is too large: %d", len(points))
	}
	points = append([]sqliteV4BlockPoint(nil), points...)
	sort.SliceStable(points, func(i, j int) bool { return points[i].timestamp < points[j].timestamp })
	for i := 1; i < len(points); i++ {
		if points[i].timestamp <= points[i-1].timestamp {
			return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V4 block timestamps must be strictly increasing")
		}
	}

	var raw bytes.Buffer
	raw.WriteString(sqliteV4BlockMagic)
	appendUvarintTo(&raw, uint64(len(points)))
	appendVarintTo(&raw, points[0].timestamp)
	if len(points) > 1 {
		delta, ok := checkedSubInt64(points[1].timestamp, points[0].timestamp)
		if !ok {
			return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V4 timestamp delta overflow")
		}
		appendVarintTo(&raw, delta)
		previousDelta := delta
		for i := 2; i < len(points); i++ {
			delta, ok = checkedSubInt64(points[i].timestamp, points[i-1].timestamp)
			if !ok {
				return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V4 timestamp delta overflow")
			}
			deltaOfDelta, ok := checkedSubInt64(delta, previousDelta)
			if !ok {
				return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V4 timestamp delta-of-delta overflow")
			}
			appendVarintTo(&raw, deltaOfDelta)
			previousDelta = delta
		}
	}

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
			significant := 64 - previousLeading - previousTrailing
			valueWriter.writeBits(xor>>previousTrailing, significant)
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
			return sqliteV4EncodedBlock{}, fmt.Errorf("metric: SQLite V4 creation-time delta overflow")
		}
		appendVarintTo(&raw, delta)
	}

	payload := append([]byte{sqliteV4PayloadRaw}, raw.Bytes()...)
	var compressed bytes.Buffer
	compressed.WriteByte(sqliteV4PayloadDeflate)
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		return sqliteV4EncodedBlock{}, err
	}
	if _, err := writer.Write(raw.Bytes()); err != nil {
		_ = writer.Close()
		return sqliteV4EncodedBlock{}, err
	}
	if err := writer.Close(); err != nil {
		return sqliteV4EncodedBlock{}, err
	}
	if compressed.Len() < len(payload) {
		payload = compressed.Bytes()
	}

	return sqliteV4EncodedBlock{
		startNano: points[0].timestamp,
		endNano:   points[len(points)-1].timestamp,
		count:     len(points),
		codec:     sqliteV4BlockCodec,
		checksum:  crc32.ChecksumIEEE(payload),
		payload:   append([]byte(nil), payload...),
	}, nil
}
