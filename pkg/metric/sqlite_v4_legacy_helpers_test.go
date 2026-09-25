package metric

import (
	"bytes"
	"compress/flate"
	"fmt"
	"hash/crc32"
	"sort"
)

func encodeSQLiteV4LegacyRollupBlock(records []sqliteV4RollupRecord) (sqliteV4EncodedRollupBlock, error) {
	if len(records) == 0 {
		return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: cannot encode an empty SQLite V4 rollup block")
	}
	if len(records) > sqliteV4MaxDecodedRollupRows {
		return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: SQLite V4 rollup block is too large: %d", len(records))
	}
	records = append([]sqliteV4RollupRecord(nil), records...)
	sort.SliceStable(records, func(i, j int) bool { return records[i].bucketNano < records[j].bucketNano })
	for i := 1; i < len(records); i++ {
		if records[i].bucketNano <= records[i-1].bucketNano {
			return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: SQLite V4 rollup buckets must be strictly increasing")
		}
	}

	var raw bytes.Buffer
	raw.WriteString(sqliteV4RollupBlockMagic)
	appendUvarintTo(&raw, uint64(len(records)))
	if err := encodeSQLiteV4RollupBuckets(&raw, records); err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	for _, record := range records {
		if record.count < 0 {
			return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: negative SQLite V4 rollup count")
		}
		appendUvarintTo(&raw, uint64(record.count))
	}

	floatFields := [sqliteV4RollupFloatFieldCount]func(sqliteV4RollupRecord) uint64{
		func(record sqliteV4RollupRecord) uint64 { return record.sumBits },
		func(record sqliteV4RollupRecord) uint64 { return record.sumSqBits },
		func(record sqliteV4RollupRecord) uint64 { return record.minBits },
		func(record sqliteV4RollupRecord) uint64 { return record.maxBits },
		func(record sqliteV4RollupRecord) uint64 { return record.firstBits },
		func(record sqliteV4RollupRecord) uint64 { return record.lastBits },
	}
	for _, field := range floatFields {
		values := make([]uint64, len(records))
		for i, record := range records {
			values[i] = field(record)
		}
		encoded, bitCount := encodeSQLiteV4FloatBits(values)
		appendUvarintTo(&raw, uint64(bitCount))
		raw.Write(encoded)
	}

	for _, record := range records {
		firstOffset, ok := checkedSubInt64(record.firstTS, record.bucketNano)
		if !ok {
			return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: SQLite V4 rollup first timestamp offset overflow")
		}
		lastOffset, ok := checkedSubInt64(record.lastTS, record.bucketNano)
		if !ok {
			return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: SQLite V4 rollup last timestamp offset overflow")
		}
		appendVarintTo(&raw, firstOffset)
		appendVarintTo(&raw, lastOffset)
	}
	for _, record := range records {
		appendUvarintTo(&raw, uint64(len(record.digest)))
		raw.Write(record.digest)
	}
	appendVarintTo(&raw, records[0].createdAt)
	for i := 1; i < len(records); i++ {
		delta, ok := checkedSubInt64(records[i].createdAt, records[i-1].createdAt)
		if !ok {
			return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: SQLite V4 rollup creation-time delta overflow")
		}
		appendVarintTo(&raw, delta)
	}

	payload := append([]byte{sqliteV4PayloadRaw}, raw.Bytes()...)
	var compressed bytes.Buffer
	compressed.WriteByte(sqliteV4PayloadDeflate)
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	if _, err := writer.Write(raw.Bytes()); err != nil {
		_ = writer.Close()
		return sqliteV4EncodedRollupBlock{}, err
	}
	if err := writer.Close(); err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	if compressed.Len() < len(payload) {
		payload = compressed.Bytes()
	}
	return sqliteV4EncodedRollupBlock{
		startNano: records[0].bucketNano,
		endNano:   records[len(records)-1].bucketNano,
		count:     len(records),
		codec:     sqliteV4LegacyRollupBlockCodec,
		checksum:  crc32.ChecksumIEEE(payload),
		payload:   append([]byte(nil), payload...),
	}, nil
}
