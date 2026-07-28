package metric

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"sort"
)

const (
	sqliteV4RollupSummaryMagic       = "KMS4"
	sqliteV4RollupDigestMagic        = "KMD4"
	sqliteV4RollupDigestCompactMagic = "KMX4"
	sqliteV4RollupDigestDenseMagic   = "KMY4"
	sqliteV4LegacyRollupDigestCodec  = 1
	sqliteV4CompactRollupDigestCodec = 2
	sqliteV4RollupDigestCodec        = 3
	sqliteV4RollupSummaryLevel       = 3
	sqliteV4RollupDigestLevel        = 6
)

const (
	sqliteV4DigestMetadataFromSummary = byte(1 << 0)
	sqliteV4DigestIntegerWeights      = byte(1 << 1)
	sqliteV4DigestPresent             = byte(1 << 2)
	sqliteV4DigestXORMeans            = byte(1 << 3)
	sqliteV4DigestCommonCompression   = byte(1 << 0)
)

func encodeSQLiteV4RollupBlockV2(records []sqliteV4RollupRecord) (sqliteV4EncodedRollupBlock, error) {
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

	var summary bytes.Buffer
	summary.WriteString(sqliteV4RollupSummaryMagic)
	appendUvarintTo(&summary, uint64(len(records)))
	if err := encodeSQLiteV4RollupBuckets(&summary, records); err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	for _, record := range records {
		if record.count < 0 {
			return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: negative SQLite V4 rollup count")
		}
		appendUvarintTo(&summary, uint64(record.count))
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
		appendUvarintTo(&summary, uint64(bitCount))
		summary.Write(encoded)
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
		appendVarintTo(&summary, firstOffset)
		appendVarintTo(&summary, lastOffset)
	}
	appendVarintTo(&summary, records[0].createdAt)
	for i := 1; i < len(records); i++ {
		delta, ok := checkedSubInt64(records[i].createdAt, records[i-1].createdAt)
		if !ok {
			return sqliteV4EncodedRollupBlock{}, fmt.Errorf("metric: SQLite V4 rollup creation-time delta overflow")
		}
		appendVarintTo(&summary, delta)
	}

	digestPayload, err := encodeSQLiteV4RollupDigestSection(records)
	if err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}

	payload, err := compressSQLiteV4RollupSection(summary.Bytes(), sqliteV4RollupSummaryLevel)
	if err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	return sqliteV4EncodedRollupBlock{
		startNano:      records[0].bucketNano,
		endNano:        records[len(records)-1].bucketNano,
		count:          len(records),
		codec:          sqliteV4RollupBlockCodec,
		checksum:       crc32.ChecksumIEEE(payload),
		payload:        payload,
		digestCodec:    sqliteV4RollupDigestCodec,
		digestChecksum: crc32.ChecksumIEEE(digestPayload),
		digestPayload:  digestPayload,
	}, nil
}

// encodeSQLiteV4RollupDigestSection stores a lossless dense digest layout. It
// promotes the normally constant compression value to the block header and
// removes per-digest length prefixes. Sorted centroid means use reversible
// ordered-float deltas; unusual unordered values fall back to XOR deltas.
func encodeSQLiteV4RollupDigestSection(records []sqliteV4RollupRecord) ([]byte, error) {
	type digestData struct {
		present             bool
		compressionBits     uint64
		minBits             uint64
		maxBits             uint64
		countBits           uint64
		metadataFromSummary bool
		integerWeights      bool
		xorMeans            bool
		means               []uint64
		weights             []uint64
		weightBits          []uint64
	}
	data := make([]digestData, len(records))
	commonCompression := true
	var commonCompressionBits uint64
	haveCompression := false
	for index, record := range records {
		raw, err := sqliteV4RawTDigest(record.digest)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			continue
		}
		if len(raw) < 39 || raw[0] != tdigestMagic0 || raw[1] != tdigestMagic1 || raw[2] != tdigestVersion {
			return nil, fmt.Errorf("metric: invalid raw SQLite V4 t-digest")
		}
		n := binary.LittleEndian.Uint32(raw[35:39])
		if uint64(n) > uint64((len(raw)-39)/16) || len(raw) != 39+int(n)*16 {
			return nil, fmt.Errorf("metric: invalid raw SQLite V4 t-digest length")
		}
		item := &data[index]
		item.present = true
		item.compressionBits = binary.LittleEndian.Uint64(raw[3:11])
		item.minBits = binary.LittleEndian.Uint64(raw[11:19])
		item.maxBits = binary.LittleEndian.Uint64(raw[19:27])
		item.countBits = binary.LittleEndian.Uint64(raw[27:35])
		item.metadataFromSummary = record.count >= 0 && item.minBits == record.minBits &&
			item.maxBits == record.maxBits && item.countBits == math.Float64bits(float64(record.count))
		item.integerWeights = true
		item.means = make([]uint64, n)
		item.weights = make([]uint64, n)
		item.weightBits = make([]uint64, n)
		for i := uint32(0); i < n; i++ {
			off := 39 + int(i)*16
			item.means[i] = binary.LittleEndian.Uint64(raw[off : off+8])
			item.weightBits[i] = binary.LittleEndian.Uint64(raw[off+8 : off+16])
			weight := math.Float64frombits(item.weightBits[i])
			if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) || math.Trunc(weight) != weight || weight >= math.Ldexp(1, 64) {
				item.integerWeights = false
				continue
			}
			integerWeight := uint64(weight)
			if math.Float64bits(float64(integerWeight)) != item.weightBits[i] {
				item.integerWeights = false
				continue
			}
			item.weights[i] = integerWeight
		}
		for i := 1; i < len(item.means); i++ {
			if sqliteV4OrderedFloatBits(item.means[i]) < sqliteV4OrderedFloatBits(item.means[i-1]) {
				item.xorMeans = true
				break
			}
		}
		if !haveCompression {
			commonCompressionBits = item.compressionBits
			haveCompression = true
		} else if item.compressionBits != commonCompressionBits {
			commonCompression = false
		}
	}
	if !haveCompression {
		commonCompression = false
	}

	var digests bytes.Buffer
	digests.WriteString(sqliteV4RollupDigestDenseMagic)
	appendUvarintTo(&digests, uint64(len(records)))
	blockFlags := byte(0)
	if commonCompression {
		blockFlags |= sqliteV4DigestCommonCompression
	}
	digests.WriteByte(blockFlags)
	var u64 [8]byte
	if commonCompression {
		binary.LittleEndian.PutUint64(u64[:], commonCompressionBits)
		digests.Write(u64[:])
	}
	for _, item := range data {
		if !item.present {
			digests.WriteByte(0)
			continue
		}
		flags := sqliteV4DigestPresent
		if item.metadataFromSummary {
			flags |= sqliteV4DigestMetadataFromSummary
		}
		if item.integerWeights {
			flags |= sqliteV4DigestIntegerWeights
		}
		if item.xorMeans {
			flags |= sqliteV4DigestXORMeans
		}
		digests.WriteByte(flags)
		if !commonCompression {
			binary.LittleEndian.PutUint64(u64[:], item.compressionBits)
			digests.Write(u64[:])
		}
		if !item.metadataFromSummary {
			for _, value := range []uint64{item.minBits, item.maxBits, item.countBits} {
				binary.LittleEndian.PutUint64(u64[:], value)
				digests.Write(u64[:])
			}
		}
		appendUvarintTo(&digests, uint64(len(item.means)))
		if len(item.means) > 0 {
			first := item.means[0]
			if !item.xorMeans {
				first = sqliteV4OrderedFloatBits(first)
			}
			binary.LittleEndian.PutUint64(u64[:], first)
			digests.Write(u64[:])
			previous := item.means[0]
			for i := 1; i < len(item.means); i++ {
				if item.xorMeans {
					appendUvarintTo(&digests, item.means[i]^previous)
				} else {
					appendUvarintTo(&digests, sqliteV4OrderedFloatBits(item.means[i])-sqliteV4OrderedFloatBits(previous))
				}
				previous = item.means[i]
			}
		}
		if item.integerWeights {
			for _, weight := range item.weights {
				appendUvarintTo(&digests, weight)
			}
		} else {
			for _, bits := range item.weightBits {
				binary.LittleEndian.PutUint64(u64[:], bits)
				digests.Write(u64[:])
			}
		}
	}
	return compressSQLiteV4RollupSection(digests.Bytes(), sqliteV4RollupDigestLevel)
}

func sqliteV4OrderedFloatBits(bits uint64) uint64 {
	if bits&(uint64(1)<<63) != 0 {
		return ^bits
	}
	return bits ^ (uint64(1) << 63)
}

func sqliteV4FloatBitsFromOrdered(ordered uint64) uint64 {
	if ordered&(uint64(1)<<63) != 0 {
		return ordered ^ (uint64(1) << 63)
	}
	return ^ordered
}

func encodeSQLiteV4CompactTDigest(record sqliteV4RollupRecord) ([]byte, error) {
	raw, err := sqliteV4RawTDigest(record.digest)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) < 39 || raw[0] != tdigestMagic0 || raw[1] != tdigestMagic1 || raw[2] != tdigestVersion {
		return nil, fmt.Errorf("metric: invalid raw SQLite V4 t-digest")
	}
	n := binary.LittleEndian.Uint32(raw[35:39])
	if uint64(n) > uint64((len(raw)-39)/16) || len(raw) != 39+int(n)*16 {
		return nil, fmt.Errorf("metric: invalid raw SQLite V4 t-digest length")
	}

	compressionBits := binary.LittleEndian.Uint64(raw[3:11])
	minBits := binary.LittleEndian.Uint64(raw[11:19])
	maxBits := binary.LittleEndian.Uint64(raw[19:27])
	countBits := binary.LittleEndian.Uint64(raw[27:35])
	metadataFromSummary := record.count >= 0 &&
		minBits == record.minBits && maxBits == record.maxBits &&
		countBits == math.Float64bits(float64(record.count))
	integerWeights := true
	weights := make([]uint64, n)
	means := make([]uint64, n)
	for i := uint32(0); i < n; i++ {
		off := 39 + int(i)*16
		means[i] = binary.LittleEndian.Uint64(raw[off : off+8])
		weight := math.Float64frombits(binary.LittleEndian.Uint64(raw[off+8 : off+16]))
		// Only use the varint representation when converting through uint64 is
		// exactly reversible at the float64 bit level. This excludes values at
		// and above 2^64 as well as large integers that cannot round-trip.
		if weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) || math.Trunc(weight) != weight || weight >= math.Ldexp(1, 64) {
			integerWeights = false
			continue
		}
		integerWeight := uint64(weight)
		if math.Float64bits(float64(integerWeight)) != math.Float64bits(weight) {
			integerWeights = false
			continue
		}
		weights[i] = integerWeight
	}

	var compact bytes.Buffer
	var u64 [8]byte
	binary.LittleEndian.PutUint64(u64[:], compressionBits)
	compact.Write(u64[:])
	flags := byte(0)
	if metadataFromSummary {
		flags |= sqliteV4DigestMetadataFromSummary
	}
	if integerWeights {
		flags |= sqliteV4DigestIntegerWeights
	}
	compact.WriteByte(flags)
	if !metadataFromSummary {
		binary.LittleEndian.PutUint64(u64[:], minBits)
		compact.Write(u64[:])
		binary.LittleEndian.PutUint64(u64[:], maxBits)
		compact.Write(u64[:])
		binary.LittleEndian.PutUint64(u64[:], countBits)
		compact.Write(u64[:])
	}
	appendUvarintTo(&compact, uint64(n))
	if n > 0 {
		binary.LittleEndian.PutUint64(u64[:], means[0])
		compact.Write(u64[:])
		previous := means[0]
		for i := uint32(1); i < n; i++ {
			appendUvarintTo(&compact, means[i]^previous)
			previous = means[i]
		}
	}
	if integerWeights {
		for _, weight := range weights {
			appendUvarintTo(&compact, weight)
		}
	} else {
		for i := uint32(0); i < n; i++ {
			off := 39 + int(i)*16 + 8
			compact.Write(raw[off : off+8])
		}
	}
	return compact.Bytes(), nil
}

func decodeSQLiteV4RollupBlock(codec, expectedCount int, expectedChecksum uint32, payload []byte, digestCodec int, expectedDigestChecksum uint32, digestPayload []byte, needDigest bool) ([]sqliteV4RollupRecord, error) {
	if codec == sqliteV4LegacyRollupBlockCodec {
		return decodeSQLiteV4LegacyRollupBlock(codec, expectedCount, expectedChecksum, payload)
	}
	if codec != sqliteV4RollupBlockCodec {
		return nil, fmt.Errorf("metric: unsupported SQLite V4 rollup block codec %d", codec)
	}
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return nil, fmt.Errorf("metric: SQLite V4 rollup summary checksum mismatch")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(raw)
	magic := make([]byte, len(sqliteV4RollupSummaryMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != sqliteV4RollupSummaryMagic {
		return nil, fmt.Errorf("metric: invalid SQLite V4 rollup summary header")
	}
	count64, err := binary.ReadUvarint(reader)
	if err != nil || count64 == 0 || count64 > sqliteV4MaxDecodedRollupRows {
		return nil, fmt.Errorf("metric: invalid SQLite V4 rollup row count")
	}
	count := int(count64)
	if expectedCount >= 0 && count != expectedCount {
		return nil, fmt.Errorf("metric: SQLite V4 rollup count mismatch: header=%d row=%d", count, expectedCount)
	}
	records := make([]sqliteV4RollupRecord, count)
	if err := decodeSQLiteV4RollupBuckets(reader, records); err != nil {
		return nil, err
	}
	for i := range records {
		value, err := binary.ReadUvarint(reader)
		if err != nil || value > uint64(math.MaxInt64) {
			return nil, fmt.Errorf("metric: invalid SQLite V4 rollup count")
		}
		records[i].count = int64(value)
	}
	floatTargets := [sqliteV4RollupFloatFieldCount]func(*sqliteV4RollupRecord, uint64){
		func(record *sqliteV4RollupRecord, value uint64) { record.sumBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.sumSqBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.minBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.maxBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.firstBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.lastBits = value },
	}
	for _, assign := range floatTargets {
		bitCount, err := binary.ReadUvarint(reader)
		if err != nil || bitCount < 64 || bitCount > uint64(reader.Len())*8 {
			return nil, fmt.Errorf("metric: invalid SQLite V4 rollup float stream")
		}
		byteCount := int((bitCount + 7) / 8)
		encoded := make([]byte, byteCount)
		if _, err := io.ReadFull(reader, encoded); err != nil {
			return nil, err
		}
		values, err := decodeSQLiteV4FloatBits(encoded, int(bitCount), count)
		if err != nil {
			return nil, err
		}
		for i, value := range values {
			assign(&records[i], value)
		}
	}
	for i := range records {
		firstOffset, err := binary.ReadVarint(reader)
		if err != nil {
			return nil, fmt.Errorf("metric: decode SQLite V4 rollup first timestamp: %w", err)
		}
		lastOffset, err := binary.ReadVarint(reader)
		if err != nil {
			return nil, fmt.Errorf("metric: decode SQLite V4 rollup last timestamp: %w", err)
		}
		records[i].firstTS, err = checkedAddInt64(records[i].bucketNano, firstOffset)
		if err != nil {
			return nil, err
		}
		records[i].lastTS, err = checkedAddInt64(records[i].bucketNano, lastOffset)
		if err != nil {
			return nil, err
		}
	}
	records[0].createdAt, err = binary.ReadVarint(reader)
	if err != nil {
		return nil, fmt.Errorf("metric: decode SQLite V4 rollup creation time: %w", err)
	}
	for i := 1; i < len(records); i++ {
		delta, err := binary.ReadVarint(reader)
		if err != nil {
			return nil, fmt.Errorf("metric: decode SQLite V4 rollup creation-time delta: %w", err)
		}
		records[i].createdAt, err = checkedAddInt64(records[i-1].createdAt, delta)
		if err != nil {
			return nil, err
		}
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("metric: SQLite V4 rollup summary contains trailing data")
	}
	if needDigest {
		if err := decodeSQLiteV4RollupDigestSection(records, digestCodec, expectedDigestChecksum, digestPayload); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func decodeSQLiteV4RollupDigestSection(records []sqliteV4RollupRecord, codec int, expectedChecksum uint32, payload []byte) error {
	switch codec {
	case sqliteV4LegacyRollupDigestCodec:
		return decodeSQLiteV4LegacyRollupDigestSection(records, expectedChecksum, payload)
	case sqliteV4CompactRollupDigestCodec:
		return decodeSQLiteV4CompactRollupDigestSection(records, expectedChecksum, payload)
	case sqliteV4RollupDigestCodec:
		return decodeSQLiteV4DenseRollupDigestSection(records, expectedChecksum, payload)
	default:
		return fmt.Errorf("metric: unsupported SQLite V4 rollup digest codec %d", codec)
	}
}

func decodeSQLiteV4LegacyRollupDigestSection(records []sqliteV4RollupRecord, expectedChecksum uint32, payload []byte) error {
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return fmt.Errorf("metric: SQLite V4 rollup digest checksum mismatch")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(raw)
	magic := make([]byte, len(sqliteV4RollupDigestMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != sqliteV4RollupDigestMagic {
		return fmt.Errorf("metric: invalid SQLite V4 rollup digest header")
	}
	count, err := binary.ReadUvarint(reader)
	if err != nil || count != uint64(len(records)) {
		return fmt.Errorf("metric: SQLite V4 rollup digest count mismatch")
	}
	for i := range records {
		length, err := binary.ReadUvarint(reader)
		if err != nil || length > uint64(reader.Len()) {
			return fmt.Errorf("metric: invalid SQLite V4 rollup digest length")
		}
		records[i].digest = make([]byte, int(length))
		if _, err := io.ReadFull(reader, records[i].digest); err != nil {
			return err
		}
	}
	if reader.Len() != 0 {
		return fmt.Errorf("metric: SQLite V4 rollup digest contains trailing data")
	}
	return nil
}

func decodeSQLiteV4CompactRollupDigestSection(records []sqliteV4RollupRecord, expectedChecksum uint32, payload []byte) error {
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return fmt.Errorf("metric: SQLite V4 rollup digest checksum mismatch")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(raw)
	magic := make([]byte, len(sqliteV4RollupDigestCompactMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != sqliteV4RollupDigestCompactMagic {
		return fmt.Errorf("metric: invalid compact SQLite V4 rollup digest header")
	}
	count, err := binary.ReadUvarint(reader)
	if err != nil || count != uint64(len(records)) {
		return fmt.Errorf("metric: SQLite V4 compact rollup digest count mismatch")
	}
	for i := range records {
		length, err := binary.ReadUvarint(reader)
		if err != nil || length > uint64(reader.Len()) {
			return fmt.Errorf("metric: invalid compact SQLite V4 rollup digest length")
		}
		encoded := make([]byte, int(length))
		if _, err := io.ReadFull(reader, encoded); err != nil {
			return err
		}
		if len(encoded) == 0 {
			records[i].digest = nil
			continue
		}
		digest, err := decodeSQLiteV4CompactTDigest(encoded, records[i])
		if err != nil {
			return fmt.Errorf("metric: decode compact SQLite V4 rollup digest %d: %w", i, err)
		}
		records[i].digest = digest
	}
	if reader.Len() != 0 {
		return fmt.Errorf("metric: SQLite V4 compact rollup digest contains trailing data")
	}
	return nil
}

func decodeSQLiteV4CompactTDigest(encoded []byte, record sqliteV4RollupRecord) ([]byte, error) {
	if len(encoded) < 9 {
		return nil, fmt.Errorf("metric: compact t-digest is truncated")
	}
	reader := bytes.NewReader(encoded)
	var fixed [8]byte
	if _, err := io.ReadFull(reader, fixed[:]); err != nil {
		return nil, err
	}
	compressionBits := binary.LittleEndian.Uint64(fixed[:])
	flags, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if flags & ^(sqliteV4DigestMetadataFromSummary|sqliteV4DigestIntegerWeights) != 0 {
		return nil, fmt.Errorf("metric: unsupported compact t-digest flags 0x%x", flags)
	}
	minBits := record.minBits
	maxBits := record.maxBits
	countBits := math.Float64bits(float64(record.count))
	if flags&sqliteV4DigestMetadataFromSummary == 0 {
		if _, err := io.ReadFull(reader, fixed[:]); err != nil {
			return nil, err
		}
		minBits = binary.LittleEndian.Uint64(fixed[:])
		if _, err := io.ReadFull(reader, fixed[:]); err != nil {
			return nil, err
		}
		maxBits = binary.LittleEndian.Uint64(fixed[:])
		if _, err := io.ReadFull(reader, fixed[:]); err != nil {
			return nil, err
		}
		countBits = binary.LittleEndian.Uint64(fixed[:])
	}
	n, err := binary.ReadUvarint(reader)
	if err != nil || n > uint64(sqliteV4MaxDecodedRollupRows) || n > uint64(reader.Len()+1) {
		return nil, fmt.Errorf("metric: invalid compact t-digest centroid count")
	}
	means := make([]uint64, int(n))
	if n > 0 {
		if _, err := io.ReadFull(reader, fixed[:]); err != nil {
			return nil, err
		}
		means[0] = binary.LittleEndian.Uint64(fixed[:])
		for i := 1; i < int(n); i++ {
			delta, err := binary.ReadUvarint(reader)
			if err != nil {
				return nil, err
			}
			means[i] = means[i-1] ^ delta
		}
	}
	weights := make([]uint64, int(n))
	weightBits := make([]uint64, int(n))
	if flags&sqliteV4DigestIntegerWeights != 0 {
		for i := range weights {
			weight, err := binary.ReadUvarint(reader)
			if err != nil {
				return nil, err
			}
			weights[i] = weight
			weightBits[i] = math.Float64bits(float64(weight))
		}
	} else {
		for i := range weightBits {
			if _, err := io.ReadFull(reader, fixed[:]); err != nil {
				return nil, err
			}
			weightBits[i] = binary.LittleEndian.Uint64(fixed[:])
		}
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("metric: compact t-digest contains trailing data")
	}

	raw := make([]byte, 0, 39+int(n)*16)
	raw = append(raw, tdigestMagic0, tdigestMagic1, tdigestVersion)
	var u64 [8]byte
	for _, value := range []uint64{compressionBits, minBits, maxBits, countBits} {
		binary.LittleEndian.PutUint64(u64[:], value)
		raw = append(raw, u64[:]...)
	}
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(n))
	raw = append(raw, u32[:]...)
	for i, meanBits := range means {
		binary.LittleEndian.PutUint64(u64[:], meanBits)
		raw = append(raw, u64[:]...)
		binary.LittleEndian.PutUint64(u64[:], weightBits[i])
		raw = append(raw, u64[:]...)
	}
	return raw, nil
}

func decodeSQLiteV4DenseRollupDigestSection(records []sqliteV4RollupRecord, expectedChecksum uint32, payload []byte) error {
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return fmt.Errorf("metric: SQLite V4 dense rollup digest checksum mismatch")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(raw)
	magic := make([]byte, len(sqliteV4RollupDigestDenseMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != sqliteV4RollupDigestDenseMagic {
		return fmt.Errorf("metric: invalid dense SQLite V4 rollup digest header")
	}
	count, err := binary.ReadUvarint(reader)
	if err != nil || count != uint64(len(records)) {
		return fmt.Errorf("metric: SQLite V4 dense rollup digest count mismatch")
	}
	blockFlags, err := reader.ReadByte()
	if err != nil || blockFlags & ^sqliteV4DigestCommonCompression != 0 {
		return fmt.Errorf("metric: unsupported dense SQLite V4 digest block flags 0x%x", blockFlags)
	}
	var fixed [8]byte
	var commonCompressionBits uint64
	if blockFlags&sqliteV4DigestCommonCompression != 0 {
		if _, err := io.ReadFull(reader, fixed[:]); err != nil {
			return err
		}
		commonCompressionBits = binary.LittleEndian.Uint64(fixed[:])
	}
	for index := range records {
		flags, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if flags & ^(sqliteV4DigestMetadataFromSummary|sqliteV4DigestIntegerWeights|sqliteV4DigestPresent|sqliteV4DigestXORMeans) != 0 {
			return fmt.Errorf("metric: unsupported dense SQLite V4 digest flags 0x%x", flags)
		}
		if flags&sqliteV4DigestPresent == 0 {
			if flags != 0 {
				return fmt.Errorf("metric: invalid empty dense SQLite V4 digest flags 0x%x", flags)
			}
			records[index].digest = nil
			continue
		}
		compressionBits := commonCompressionBits
		if blockFlags&sqliteV4DigestCommonCompression == 0 {
			if _, err := io.ReadFull(reader, fixed[:]); err != nil {
				return err
			}
			compressionBits = binary.LittleEndian.Uint64(fixed[:])
		}
		minBits := records[index].minBits
		maxBits := records[index].maxBits
		countBits := math.Float64bits(float64(records[index].count))
		if flags&sqliteV4DigestMetadataFromSummary == 0 {
			if _, err := io.ReadFull(reader, fixed[:]); err != nil {
				return err
			}
			minBits = binary.LittleEndian.Uint64(fixed[:])
			if _, err := io.ReadFull(reader, fixed[:]); err != nil {
				return err
			}
			maxBits = binary.LittleEndian.Uint64(fixed[:])
			if _, err := io.ReadFull(reader, fixed[:]); err != nil {
				return err
			}
			countBits = binary.LittleEndian.Uint64(fixed[:])
		}
		n, err := binary.ReadUvarint(reader)
		if err != nil || n > uint64(sqliteV4MaxDecodedRollupRows) || n > uint64(reader.Len()+1) {
			return fmt.Errorf("metric: invalid dense SQLite V4 t-digest centroid count")
		}
		means := make([]uint64, int(n))
		if n > 0 {
			if _, err := io.ReadFull(reader, fixed[:]); err != nil {
				return err
			}
			first := binary.LittleEndian.Uint64(fixed[:])
			if flags&sqliteV4DigestXORMeans != 0 {
				means[0] = first
				for i := 1; i < int(n); i++ {
					delta, err := binary.ReadUvarint(reader)
					if err != nil {
						return err
					}
					means[i] = means[i-1] ^ delta
				}
			} else {
				ordered := first
				means[0] = sqliteV4FloatBitsFromOrdered(ordered)
				for i := 1; i < int(n); i++ {
					delta, err := binary.ReadUvarint(reader)
					if err != nil {
						return fmt.Errorf("metric: invalid dense SQLite V4 centroid delta")
					}
					next := ordered + delta
					if next < ordered {
						return fmt.Errorf("metric: dense SQLite V4 centroid delta overflow")
					}
					ordered = next
					means[i] = sqliteV4FloatBitsFromOrdered(ordered)
				}
			}
		}
		weightBits := make([]uint64, int(n))
		if flags&sqliteV4DigestIntegerWeights != 0 {
			for i := range weightBits {
				weight, err := binary.ReadUvarint(reader)
				if err != nil {
					return err
				}
				weightBits[i] = math.Float64bits(float64(weight))
			}
		} else {
			for i := range weightBits {
				if _, err := io.ReadFull(reader, fixed[:]); err != nil {
					return err
				}
				weightBits[i] = binary.LittleEndian.Uint64(fixed[:])
			}
		}
		digest := make([]byte, 0, 39+int(n)*16)
		digest = append(digest, tdigestMagic0, tdigestMagic1, tdigestVersion)
		for _, value := range []uint64{compressionBits, minBits, maxBits, countBits} {
			binary.LittleEndian.PutUint64(fixed[:], value)
			digest = append(digest, fixed[:]...)
		}
		var u32 [4]byte
		binary.LittleEndian.PutUint32(u32[:], uint32(n))
		digest = append(digest, u32[:]...)
		for i, meanBits := range means {
			binary.LittleEndian.PutUint64(fixed[:], meanBits)
			digest = append(digest, fixed[:]...)
			binary.LittleEndian.PutUint64(fixed[:], weightBits[i])
			digest = append(digest, fixed[:]...)
		}
		records[index].digest = digest
	}
	if reader.Len() != 0 {
		return fmt.Errorf("metric: dense SQLite V4 rollup digest contains trailing data")
	}
	return nil
}

func sqliteV4RawTDigest(encoded []byte) ([]byte, error) {
	if len(encoded) == 0 {
		return nil, nil
	}
	digest, err := DecodeTDigest(encoded)
	if err != nil {
		return nil, fmt.Errorf("metric: decode SQLite V4 t-digest: %w", err)
	}
	return digest.encodeRaw(), nil
}

func compressSQLiteV4RollupSection(raw []byte, level int) ([]byte, error) {
	payload := append([]byte{sqliteV4PayloadRaw}, raw...)
	var compressed bytes.Buffer
	compressed.WriteByte(sqliteV4PayloadDeflate)
	writer, err := flate.NewWriter(&compressed, level)
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

func sqliteV4TDigestsEqual(left, right []byte) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == 0 && len(right) == 0
	}
	a, err := DecodeTDigest(left)
	if err != nil {
		return false
	}
	b, err := DecodeTDigest(right)
	if err != nil {
		return false
	}
	if math.Float64bits(a.compression) != math.Float64bits(b.compression) ||
		math.Float64bits(a.count) != math.Float64bits(b.count) ||
		math.Float64bits(a.min) != math.Float64bits(b.min) ||
		math.Float64bits(a.max) != math.Float64bits(b.max) || len(a.centroids) != len(b.centroids) {
		return false
	}
	for i := range a.centroids {
		if math.Float64bits(a.centroids[i].mean) != math.Float64bits(b.centroids[i].mean) ||
			math.Float64bits(a.centroids[i].weight) != math.Float64bits(b.centroids[i].weight) {
			return false
		}
	}
	return true
}
