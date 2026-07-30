package metric

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"sort"
)

const (
	sqliteV4SharedRollupBlockCodec        = 3
	sqliteV4LegacyStructuredDigestCodec   = 4
	sqliteV4StructuredRollupDigestCodec   = 5
	sqliteV4LegacyRollupAxisCodec         = 1
	sqliteV4LegacyRollupAxisMagic         = "KMA5"
	sqliteV4RollupAxisCodec               = 2
	sqliteV4RollupAxisMagic               = "KMA8"
	sqliteV4RollupValueMagic              = "KMV5"
	sqliteV4RollupStructuredDigestMagic   = "KMZ9"
	sqliteV4LegacyStructuredDigestMagic   = "KMZ5"
	sqliteV4ValueRaw                      = byte(0)
	sqliteV4ValueInteger                  = byte(1)
	sqliteV4StructuredDigestPresent       = uint64(1 << 0)
	sqliteV4StructuredDigestMetadata      = uint64(1 << 1)
	sqliteV4StructuredDigestUnitWeights   = uint64(1 << 2)
	sqliteV4StructuredDigestIntegerWeight = uint64(1 << 3)
	sqliteV4StructuredDigestMeanInteger   = uint64(1 << 4)
	sqliteV4StructuredDigestMeanDecimal   = uint64(1 << 5)
	sqliteV4StructuredDigestMeanXOR       = uint64(1 << 6)
	sqliteV4StructuredDigestMeanRelative  = uint64(1 << 7)
	sqliteV4StructuredDigestRepeatWeights = uint64(1 << 8)
	sqliteV4StructuredDigestCommonComp    = byte(1 << 0)
)

func encodeSQLiteV4RollupBlock(records []sqliteV4RollupRecord) (sqliteV4EncodedRollupBlock, error) {
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

	axisPayload, err := encodeSQLiteV4RollupAxis(records)
	if err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	valuePayload, err := encodeSQLiteV4RollupValues(records)
	if err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	digestPayload, err := encodeSQLiteV4StructuredRollupDigests(records)
	if err != nil {
		return sqliteV4EncodedRollupBlock{}, err
	}
	return sqliteV4EncodedRollupBlock{
		startNano:      records[0].bucketNano,
		endNano:        records[len(records)-1].bucketNano,
		count:          len(records),
		codec:          sqliteV4SharedRollupBlockCodec,
		checksum:       crc32.ChecksumIEEE(valuePayload),
		payload:        valuePayload,
		axisCodec:      sqliteV4RollupAxisCodec,
		axisChecksum:   crc32.ChecksumIEEE(axisPayload),
		axisPayload:    axisPayload,
		digestCodec:    sqliteV4StructuredRollupDigestCodec,
		digestChecksum: crc32.ChecksumIEEE(digestPayload),
		digestPayload:  digestPayload,
	}, nil
}

func encodeSQLiteV4RollupAxis(records []sqliteV4RollupRecord) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString(sqliteV4RollupAxisMagic)
	appendUvarintTo(&raw, uint64(len(records)))
	if err := encodeSQLiteV4RollupBuckets(&raw, records); err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.count < 0 {
			return nil, fmt.Errorf("metric: negative SQLite V4 rollup count")
		}
		appendUvarintTo(&raw, uint64(record.count))
	}
	for _, record := range records {
		if record.lossCount < 0 || record.lossCount > record.count {
			return nil, fmt.Errorf("metric: invalid SQLite V8 rollup loss count")
		}
		appendUvarintTo(&raw, uint64(record.lossCount))
	}
	for _, record := range records {
		firstOffset, ok := checkedSubInt64(record.firstTS, record.bucketNano)
		if !ok {
			return nil, fmt.Errorf("metric: SQLite V4 first timestamp offset overflow")
		}
		lastOffset, ok := checkedSubInt64(record.lastTS, record.bucketNano)
		if !ok {
			return nil, fmt.Errorf("metric: SQLite V4 last timestamp offset overflow")
		}
		appendVarintTo(&raw, firstOffset)
		appendVarintTo(&raw, lastOffset)
	}
	return compressSQLiteV4RollupSection(raw.Bytes(), sqliteV4RollupSummaryLevel)
}

func decodeSQLiteV4RollupAxis(codec, expectedCount int, expectedChecksum uint32, payload []byte) ([]sqliteV4RollupRecord, error) {
	if (codec != sqliteV4RollupAxisCodec && codec != sqliteV4LegacyRollupAxisCodec) || len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return nil, fmt.Errorf("metric: invalid SQLite V4 shared rollup axis")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(raw)
	wantMagic := sqliteV4RollupAxisMagic
	if codec == sqliteV4LegacyRollupAxisCodec {
		wantMagic = sqliteV4LegacyRollupAxisMagic
	}
	magic := make([]byte, len(wantMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != wantMagic {
		return nil, fmt.Errorf("metric: invalid SQLite V4 rollup axis header")
	}
	count64, err := binary.ReadUvarint(reader)
	if err != nil || count64 == 0 || count64 > sqliteV4MaxDecodedRollupRows {
		return nil, fmt.Errorf("metric: invalid SQLite V4 rollup axis count")
	}
	count := int(count64)
	if expectedCount >= 0 && count != expectedCount {
		return nil, fmt.Errorf("metric: SQLite V4 rollup axis count mismatch: header=%d row=%d", count, expectedCount)
	}
	records := make([]sqliteV4RollupRecord, count)
	if err := decodeSQLiteV4RollupBuckets(reader, records); err != nil {
		return nil, err
	}
	for i := range records {
		value, err := binary.ReadUvarint(reader)
		if err != nil || value > uint64(1<<63-1) {
			return nil, fmt.Errorf("metric: invalid SQLite V4 rollup axis sample count")
		}
		records[i].count = int64(value)
	}
	if codec == sqliteV4RollupAxisCodec {
		for i := range records {
			value, err := binary.ReadUvarint(reader)
			if err != nil || value > uint64(records[i].count) {
				return nil, fmt.Errorf("metric: invalid SQLite V8 rollup loss count")
			}
			records[i].lossCount = int64(value)
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
	if reader.Len() != 0 {
		return nil, fmt.Errorf("metric: SQLite V4 rollup axis contains trailing data")
	}
	return records, nil
}

func encodeSQLiteV4RollupValues(records []sqliteV4RollupRecord) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString(sqliteV4RollupValueMagic)
	appendUvarintTo(&raw, uint64(len(records)))
	fields := [sqliteV4RollupFloatFieldCount]func(sqliteV4RollupRecord) uint64{
		func(record sqliteV4RollupRecord) uint64 { return record.sumBits },
		func(record sqliteV4RollupRecord) uint64 { return record.sumSqBits },
		func(record sqliteV4RollupRecord) uint64 { return record.minBits },
		func(record sqliteV4RollupRecord) uint64 { return record.maxBits },
		func(record sqliteV4RollupRecord) uint64 { return record.firstBits },
		func(record sqliteV4RollupRecord) uint64 { return record.lastBits },
	}
	for _, field := range fields {
		values := make([]uint64, len(records))
		for i, record := range records {
			values[i] = field(record)
		}
		encodeSQLiteV4LosslessValueStream(&raw, values)
	}
	return compressSQLiteV4RollupSection(raw.Bytes(), sqliteV4RollupSummaryLevel)
}

func encodeSQLiteV4LosslessValueStream(dst *bytes.Buffer, bitsValues []uint64) {
	for exponent := 0; exponent <= 4; exponent++ {
		scaled := make([]int64, len(bitsValues))
		valid := true
		for i, bitsValue := range bitsValues {
			value, ok := sqliteV4ExactScaledInt(bitsValue, exponent)
			if !ok {
				valid = false
				break
			}
			scaled[i] = value
		}
		if !valid || !sqliteV4Int64DeltasFit(scaled) {
			continue
		}
		if exponent == 0 {
			dst.WriteByte(sqliteV4ValueInteger)
		} else {
			dst.WriteByte(byte(exponent + 1))
		}
		appendVarintTo(dst, scaled[0])
		for i := 1; i < len(scaled); i++ {
			appendVarintTo(dst, scaled[i]-scaled[i-1])
		}
		return
	}
	dst.WriteByte(sqliteV4ValueRaw)
	encoded, bitCount := encodeSQLiteV4FloatBits(bitsValues)
	appendUvarintTo(dst, uint64(bitCount))
	dst.Write(encoded)
}

func sqliteV4ExactScaledInt(bitsValue uint64, exponent int) (int64, bool) {
	value := math.Float64frombits(bitsValue)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	scale := math.Pow10(exponent)
	scaled := math.Round(value * scale)
	if scaled < -math.Ldexp(1, 63) || scaled >= math.Ldexp(1, 63) {
		return 0, false
	}
	integer := int64(scaled)
	if math.Float64bits(float64(integer)/scale) != bitsValue {
		return 0, false
	}
	return integer, true
}

func sqliteV4Int64DeltasFit(values []int64) bool {
	for i := 1; i < len(values); i++ {
		if _, ok := checkedSubInt64(values[i], values[i-1]); !ok {
			return false
		}
	}
	return true
}

func decodeSQLiteV4LosslessValueStream(reader *bytes.Reader, count int) ([]uint64, error) {
	mode, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	if mode == sqliteV4ValueRaw {
		bitCount, err := binary.ReadUvarint(reader)
		if err != nil || bitCount < 64 || bitCount > uint64(reader.Len())*8 {
			return nil, fmt.Errorf("metric: invalid SQLite V4 raw value stream")
		}
		byteCount := int((bitCount + 7) / 8)
		encoded := make([]byte, byteCount)
		if _, err := io.ReadFull(reader, encoded); err != nil {
			return nil, err
		}
		return decodeSQLiteV4FloatBits(encoded, int(bitCount), count)
	}
	exponent := 0
	switch {
	case mode == sqliteV4ValueInteger:
	case mode >= 2 && mode <= 5:
		exponent = int(mode - 1)
	default:
		return nil, fmt.Errorf("metric: unsupported SQLite V4 value mode %d", mode)
	}
	current, err := binary.ReadVarint(reader)
	if err != nil {
		return nil, err
	}
	result := make([]uint64, count)
	scale := math.Pow10(exponent)
	result[0] = math.Float64bits(float64(current) / scale)
	for i := 1; i < count; i++ {
		delta, err := binary.ReadVarint(reader)
		if err != nil {
			return nil, err
		}
		current, err = checkedAddInt64(current, delta)
		if err != nil {
			return nil, err
		}
		result[i] = math.Float64bits(float64(current) / scale)
	}
	return result, nil
}

func decodeSQLiteV4SharedRollupBlock(expectedCount int, expectedChecksum uint32, payload []byte, axisCodec int, expectedAxisChecksum uint32, axisPayload []byte) ([]sqliteV4RollupRecord, error) {
	records, err := decodeSQLiteV4RollupAxis(axisCodec, expectedCount, expectedAxisChecksum, axisPayload)
	if err != nil {
		return nil, err
	}
	return decodeSQLiteV4SharedRollupValues(expectedCount, expectedChecksum, payload, records)
}

func decodeSQLiteV4SharedRollupValues(expectedCount int, expectedChecksum uint32, payload []byte, records []sqliteV4RollupRecord) ([]sqliteV4RollupRecord, error) {
	if len(records) != expectedCount {
		return nil, fmt.Errorf("metric: SQLite V4 shared rollup axis count mismatch")
	}
	if len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return nil, fmt.Errorf("metric: SQLite V4 shared rollup value checksum mismatch")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(raw)
	magic := make([]byte, len(sqliteV4RollupValueMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != sqliteV4RollupValueMagic {
		return nil, fmt.Errorf("metric: invalid SQLite V4 shared rollup value header")
	}
	count64, err := binary.ReadUvarint(reader)
	if err != nil || int(count64) != len(records) {
		return nil, fmt.Errorf("metric: SQLite V4 shared rollup value count mismatch")
	}
	targets := [sqliteV4RollupFloatFieldCount]func(*sqliteV4RollupRecord, uint64){
		func(record *sqliteV4RollupRecord, value uint64) { record.sumBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.sumSqBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.minBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.maxBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.firstBits = value },
		func(record *sqliteV4RollupRecord, value uint64) { record.lastBits = value },
	}
	for _, assign := range targets {
		values, err := decodeSQLiteV4LosslessValueStream(reader, len(records))
		if err != nil {
			return nil, err
		}
		for i, value := range values {
			assign(&records[i], value)
		}
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("metric: SQLite V4 shared rollup values contain trailing data")
	}
	return records, nil
}

type sqliteV4StructuredDigestData struct {
	present             bool
	compressionBits     uint64
	minBits             uint64
	maxBits             uint64
	countBits           uint64
	metadataFromSummary bool
	means               []uint64
	weights             []uint64
	weightBits          []uint64
	meanIntegers        []int64
	meanExponent        int
	meanXOR             bool
	meanRelative        bool
	meanBaseInteger     int64
	unitWeights         bool
	integerWeights      bool
}

func equalUint64Slices(left, right []uint64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func uvarintEncodedLen(value uint64) int {
	var encoded [10]byte
	return binary.PutUvarint(encoded[:], value)
}

func varintEncodedLen(value int64) int {
	var encoded [10]byte
	return binary.PutVarint(encoded[:], value)
}

func encodeSQLiteV4StructuredRollupDigests(records []sqliteV4RollupRecord) ([]byte, error) {
	data := make([]sqliteV4StructuredDigestData, len(records))
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
		validCount := record.count - record.lossCount
		item.metadataFromSummary = item.minBits == record.minBits && item.maxBits == record.maxBits &&
			validCount >= 0 && item.countBits == math.Float64bits(float64(validCount))
		item.means = make([]uint64, n)
		item.weights = make([]uint64, n)
		item.weightBits = make([]uint64, n)
		item.unitWeights = true
		item.integerWeights = true
		for i := uint32(0); i < n; i++ {
			offset := 39 + int(i)*16
			item.means[i] = binary.LittleEndian.Uint64(raw[offset : offset+8])
			item.weightBits[i] = binary.LittleEndian.Uint64(raw[offset+8 : offset+16])
			weight := math.Float64frombits(item.weightBits[i])
			if weight != 1 {
				item.unitWeights = false
			}
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
		for exponent := 0; exponent <= 4; exponent++ {
			scaled := make([]int64, len(item.means))
			valid := true
			for i, mean := range item.means {
				value, ok := sqliteV4ExactScaledInt(mean, exponent)
				if !ok {
					valid = false
					break
				}
				scaled[i] = value
			}
			if valid && sqliteV4Int64DeltasFit(scaled) {
				item.meanIntegers = scaled
				item.meanExponent = exponent
				break
			}
		}
		if len(item.meanIntegers) == 0 {
			for i := 1; i < len(item.means); i++ {
				if sqliteV4OrderedFloatBits(item.means[i]) < sqliteV4OrderedFloatBits(item.means[i-1]) {
					item.meanXOR = true
					break
				}
			}
		}
		if len(item.meanIntegers) > 0 {
			if base, ok := sqliteV4ExactScaledInt(item.minBits, item.meanExponent); ok {
				if delta, ok := checkedSubInt64(item.meanIntegers[0], base); ok && varintEncodedLen(delta)+1 < varintEncodedLen(item.meanIntegers[0]) {
					item.meanRelative = true
					item.meanBaseInteger = base
				}
			}
		} else if len(item.means) > 0 {
			first, base := item.means[0], item.minBits
			var relative uint64
			if item.meanXOR {
				relative = first ^ base
			} else {
				orderedFirst, orderedBase := sqliteV4OrderedFloatBits(first), sqliteV4OrderedFloatBits(base)
				if orderedFirst >= orderedBase {
					relative = orderedFirst - orderedBase
				} else {
					relative = ^uint64(0)
				}
			}
			if uvarintEncodedLen(relative)+1 < 8 {
				item.meanRelative = true
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

	var raw bytes.Buffer
	raw.WriteString(sqliteV4RollupStructuredDigestMagic)
	appendUvarintTo(&raw, uint64(len(records)))
	blockFlags := byte(0)
	if commonCompression {
		blockFlags |= sqliteV4StructuredDigestCommonComp
	}
	raw.WriteByte(blockFlags)
	var u64 [8]byte
	if commonCompression {
		binary.LittleEndian.PutUint64(u64[:], commonCompressionBits)
		raw.Write(u64[:])
	}
	var previousWeights []uint64
	for _, item := range data {
		if !item.present {
			appendUvarintTo(&raw, 0)
			previousWeights = nil
			continue
		}
		flags := uint64(sqliteV4StructuredDigestPresent)
		if item.metadataFromSummary {
			flags |= sqliteV4StructuredDigestMetadata
		}
		if item.unitWeights {
			flags |= sqliteV4StructuredDigestUnitWeights
		} else if item.integerWeights {
			flags |= sqliteV4StructuredDigestIntegerWeight
		}
		if len(item.meanIntegers) > 0 {
			if item.meanExponent == 0 {
				flags |= sqliteV4StructuredDigestMeanInteger
			} else {
				flags |= sqliteV4StructuredDigestMeanDecimal
			}
		} else if item.meanXOR {
			flags |= sqliteV4StructuredDigestMeanXOR
		}
		if item.meanRelative {
			flags |= sqliteV4StructuredDigestMeanRelative
		}
		repeatWeights := !item.unitWeights && equalUint64Slices(previousWeights, item.weightBits)
		if repeatWeights {
			flags &^= sqliteV4StructuredDigestIntegerWeight
			flags |= sqliteV4StructuredDigestRepeatWeights
		}
		appendUvarintTo(&raw, flags)
		if !commonCompression {
			binary.LittleEndian.PutUint64(u64[:], item.compressionBits)
			raw.Write(u64[:])
		}
		if !item.metadataFromSummary {
			for _, value := range []uint64{item.minBits, item.maxBits, item.countBits} {
				binary.LittleEndian.PutUint64(u64[:], value)
				raw.Write(u64[:])
			}
		}
		appendUvarintTo(&raw, uint64(len(item.means)))
		if flags&sqliteV4StructuredDigestMeanDecimal != 0 {
			raw.WriteByte(byte(item.meanExponent))
		}
		if len(item.meanIntegers) > 0 {
			if len(item.meanIntegers) > 0 {
				first := item.meanIntegers[0]
				if item.meanRelative {
					first -= item.meanBaseInteger
				}
				appendVarintTo(&raw, first)
				for i := 1; i < len(item.meanIntegers); i++ {
					appendVarintTo(&raw, item.meanIntegers[i]-item.meanIntegers[i-1])
				}
			}
		} else if len(item.means) > 0 {
			first := item.means[0]
			if item.meanRelative {
				if item.meanXOR {
					appendUvarintTo(&raw, first^item.minBits)
				} else {
					appendUvarintTo(&raw, sqliteV4OrderedFloatBits(first)-sqliteV4OrderedFloatBits(item.minBits))
				}
			} else {
				if !item.meanXOR {
					first = sqliteV4OrderedFloatBits(first)
				}
				binary.LittleEndian.PutUint64(u64[:], first)
				raw.Write(u64[:])
			}
			previous := item.means[0]
			for i := 1; i < len(item.means); i++ {
				if item.meanXOR {
					appendUvarintTo(&raw, item.means[i]^previous)
				} else {
					appendUvarintTo(&raw, sqliteV4OrderedFloatBits(item.means[i])-sqliteV4OrderedFloatBits(previous))
				}
				previous = item.means[i]
			}
		}
		if item.unitWeights || repeatWeights {
			previousWeights = append(previousWeights[:0], item.weightBits...)
			continue
		}
		if item.integerWeights {
			for _, weight := range item.weights {
				appendUvarintTo(&raw, weight)
			}
			previousWeights = append(previousWeights[:0], item.weightBits...)
			continue
		}
		for _, bitsValue := range item.weightBits {
			binary.LittleEndian.PutUint64(u64[:], bitsValue)
			raw.Write(u64[:])
		}
		previousWeights = append(previousWeights[:0], item.weightBits...)
	}
	return compressSQLiteV4RollupSection(raw.Bytes(), sqliteV4RollupDigestLevel)
}

func decodeSQLiteV4StructuredRollupDigests(records []sqliteV4RollupRecord, codec int, expectedChecksum uint32, payload []byte) error {
	if (codec != sqliteV4StructuredRollupDigestCodec && codec != sqliteV4LegacyStructuredDigestCodec) || len(payload) < 2 || crc32.ChecksumIEEE(payload) != expectedChecksum {
		return fmt.Errorf("metric: invalid SQLite V4 structured digest payload")
	}
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(raw)
	version9 := codec == sqliteV4StructuredRollupDigestCodec
	wantMagic := sqliteV4LegacyStructuredDigestMagic
	if version9 {
		wantMagic = sqliteV4RollupStructuredDigestMagic
	}
	magic := make([]byte, len(wantMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != wantMagic {
		return fmt.Errorf("metric: invalid SQLite V4 structured digest header")
	}
	count64, err := binary.ReadUvarint(reader)
	if err != nil || int(count64) != len(records) {
		return fmt.Errorf("metric: SQLite V4 structured digest count mismatch")
	}
	blockFlags, err := reader.ReadByte()
	if err != nil {
		return err
	}
	if blockFlags & ^sqliteV4StructuredDigestCommonComp != 0 {
		return fmt.Errorf("metric: unsupported SQLite V4 structured digest block flags 0x%x", blockFlags)
	}
	commonCompression := blockFlags&sqliteV4StructuredDigestCommonComp != 0
	var commonCompressionBits uint64
	if commonCompression {
		if err := binary.Read(reader, binary.LittleEndian, &commonCompressionBits); err != nil {
			return err
		}
	}
	var previousWeights []uint64
	for index := range records {
		var flags uint64
		if version9 {
			flags, err = binary.ReadUvarint(reader)
		} else {
			var legacyFlags byte
			legacyFlags, err = reader.ReadByte()
			flags = uint64(legacyFlags)
		}
		if err != nil {
			return err
		}
		allowed := uint64(sqliteV4StructuredDigestPresent | sqliteV4StructuredDigestMetadata |
			sqliteV4StructuredDigestUnitWeights | sqliteV4StructuredDigestIntegerWeight |
			sqliteV4StructuredDigestMeanInteger | sqliteV4StructuredDigestMeanDecimal | sqliteV4StructuredDigestMeanXOR)
		if version9 {
			allowed |= sqliteV4StructuredDigestMeanRelative | sqliteV4StructuredDigestRepeatWeights
		}
		if flags & ^allowed != 0 {
			return fmt.Errorf("metric: unsupported SQLite V4 structured digest flags 0x%x", flags)
		}
		if flags&sqliteV4StructuredDigestPresent == 0 {
			if flags != 0 {
				return fmt.Errorf("metric: invalid empty SQLite V4 structured digest flags 0x%x", flags)
			}
			previousWeights = nil
			continue
		}
		if flags&sqliteV4StructuredDigestUnitWeights != 0 && flags&sqliteV4StructuredDigestIntegerWeight != 0 {
			return fmt.Errorf("metric: conflicting SQLite V4 structured digest weight flags 0x%x", flags)
		}
		if flags&sqliteV4StructuredDigestRepeatWeights != 0 && flags&(sqliteV4StructuredDigestUnitWeights|sqliteV4StructuredDigestIntegerWeight) != 0 {
			return fmt.Errorf("metric: conflicting SQLite V9 repeated digest weight flags 0x%x", flags)
		}
		meanFlags := flags & (sqliteV4StructuredDigestMeanInteger | sqliteV4StructuredDigestMeanDecimal | sqliteV4StructuredDigestMeanXOR)
		if meanFlags != 0 && meanFlags&(meanFlags-1) != 0 {
			return fmt.Errorf("metric: conflicting SQLite V4 structured digest mean flags 0x%x", flags)
		}
		compressionBits := commonCompressionBits
		if !commonCompression {
			if err := binary.Read(reader, binary.LittleEndian, &compressionBits); err != nil {
				return err
			}
		}
		minBits, maxBits := records[index].minBits, records[index].maxBits
		countBits := math.Float64bits(float64(records[index].count - records[index].lossCount))
		if flags&sqliteV4StructuredDigestMetadata == 0 {
			for _, target := range []*uint64{&minBits, &maxBits, &countBits} {
				if err := binary.Read(reader, binary.LittleEndian, target); err != nil {
					return err
				}
			}
		}
		n64, err := binary.ReadUvarint(reader)
		if err != nil || n64 > uint64(sqliteV4MaxDecodedRollupRows*16) {
			return fmt.Errorf("metric: invalid SQLite V4 structured digest centroid count")
		}
		n := int(n64)
		means := make([]uint64, n)
		switch {
		case flags&sqliteV4StructuredDigestMeanInteger != 0 || flags&sqliteV4StructuredDigestMeanDecimal != 0:
			exponent := 0
			if flags&sqliteV4StructuredDigestMeanDecimal != 0 {
				value, err := reader.ReadByte()
				if err != nil || value < 1 || value > 4 {
					return fmt.Errorf("metric: invalid SQLite V4 structured digest scale")
				}
				exponent = int(value)
			}
			if n > 0 {
				current, err := binary.ReadVarint(reader)
				if err != nil {
					return err
				}
				if flags&sqliteV4StructuredDigestMeanRelative != 0 {
					base, ok := sqliteV4ExactScaledInt(minBits, exponent)
					if !ok {
						return fmt.Errorf("metric: invalid SQLite V9 relative integer digest mean")
					}
					current, err = checkedAddInt64(base, current)
					if err != nil {
						return err
					}
				}
				scale := math.Pow10(exponent)
				means[0] = math.Float64bits(float64(current) / scale)
				for i := 1; i < n; i++ {
					delta, err := binary.ReadVarint(reader)
					if err != nil {
						return err
					}
					current, err = checkedAddInt64(current, delta)
					if err != nil {
						return err
					}
					means[i] = math.Float64bits(float64(current) / scale)
				}
			}
		default:
			if n > 0 {
				var first uint64
				if flags&sqliteV4StructuredDigestMeanRelative != 0 {
					relative, err := binary.ReadUvarint(reader)
					if err != nil {
						return err
					}
					if flags&sqliteV4StructuredDigestMeanXOR != 0 {
						first = minBits ^ relative
					} else {
						orderedBase := sqliteV4OrderedFloatBits(minBits)
						if orderedBase+relative < orderedBase {
							return fmt.Errorf("metric: SQLite V9 relative digest mean overflow")
						}
						first = sqliteV4FloatBitsFromOrdered(orderedBase + relative)
					}
				} else {
					if err := binary.Read(reader, binary.LittleEndian, &first); err != nil {
						return err
					}
					if flags&sqliteV4StructuredDigestMeanXOR == 0 {
						first = sqliteV4FloatBitsFromOrdered(first)
					}
				}
				means[0] = first
				previous := first
				for i := 1; i < n; i++ {
					value, err := binary.ReadUvarint(reader)
					if err != nil {
						return err
					}
					if flags&sqliteV4StructuredDigestMeanXOR != 0 {
						means[i] = previous ^ value
					} else {
						ordered := sqliteV4OrderedFloatBits(previous)
						next := ordered + value
						if next < ordered {
							return fmt.Errorf("metric: SQLite V4 structured digest mean delta overflow")
						}
						means[i] = sqliteV4FloatBitsFromOrdered(next)
					}
					previous = means[i]
				}
			}
		}
		weights := make([]uint64, n)
		switch {
		case flags&sqliteV4StructuredDigestRepeatWeights != 0:
			if len(previousWeights) != n {
				return fmt.Errorf("metric: invalid SQLite V9 repeated digest weights")
			}
			copy(weights, previousWeights)
		case flags&sqliteV4StructuredDigestUnitWeights != 0:
			for i := range weights {
				weights[i] = math.Float64bits(1)
			}
		case flags&sqliteV4StructuredDigestIntegerWeight != 0:
			for i := range weights {
				value, err := binary.ReadUvarint(reader)
				if err != nil {
					return err
				}
				weights[i] = math.Float64bits(float64(value))
			}
		default:
			for i := range weights {
				if err := binary.Read(reader, binary.LittleEndian, &weights[i]); err != nil {
					return err
				}
			}
		}
		previousWeights = append(previousWeights[:0], weights...)
		digestRaw := make([]byte, 39+n*16)
		digestRaw[0], digestRaw[1], digestRaw[2] = tdigestMagic0, tdigestMagic1, tdigestVersion
		binary.LittleEndian.PutUint64(digestRaw[3:11], compressionBits)
		binary.LittleEndian.PutUint64(digestRaw[11:19], minBits)
		binary.LittleEndian.PutUint64(digestRaw[19:27], maxBits)
		binary.LittleEndian.PutUint64(digestRaw[27:35], countBits)
		binary.LittleEndian.PutUint32(digestRaw[35:39], uint32(n))
		for i := 0; i < n; i++ {
			offset := 39 + i*16
			binary.LittleEndian.PutUint64(digestRaw[offset:offset+8], means[i])
			binary.LittleEndian.PutUint64(digestRaw[offset+8:offset+16], weights[i])
		}
		records[index].digest = digestRaw
	}
	if reader.Len() != 0 {
		return fmt.Errorf("metric: SQLite V4 structured digest contains trailing data")
	}
	return nil
}

func decodeSQLiteV4StoredRollupBlock(codec, expectedCount int, expectedChecksum uint32, payload []byte,
	axisCodec int, expectedAxisChecksum uint32, axisPayload []byte,
	digestCodec int, expectedDigestChecksum uint32, digestPayload []byte, needDigest bool,
) ([]sqliteV4RollupRecord, error) {
	if codec != sqliteV4SharedRollupBlockCodec {
		return decodeSQLiteV4RollupBlock(codec, expectedCount, expectedChecksum, payload,
			digestCodec, expectedDigestChecksum, digestPayload, needDigest)
	}
	records, err := decodeSQLiteV4SharedRollupBlock(expectedCount, expectedChecksum, payload, axisCodec, expectedAxisChecksum, axisPayload)
	if err != nil {
		return nil, err
	}
	if needDigest {
		if digestCodec == sqliteV4StructuredRollupDigestCodec || digestCodec == sqliteV4LegacyStructuredDigestCodec {
			err = decodeSQLiteV4StructuredRollupDigests(records, digestCodec, expectedDigestChecksum, digestPayload)
		} else {
			err = decodeSQLiteV4RollupDigestSection(records, digestCodec, expectedDigestChecksum, digestPayload)
		}
		if err != nil {
			return nil, err
		}
	}
	return records, nil
}

func decodeSQLiteV4EncodedRollupBlock(encoded sqliteV4EncodedRollupBlock, needDigest bool) ([]sqliteV4RollupRecord, error) {
	return decodeSQLiteV4StoredRollupBlock(
		encoded.codec, encoded.count, encoded.checksum, encoded.payload,
		encoded.axisCodec, encoded.axisChecksum, encoded.axisPayload,
		encoded.digestCodec, encoded.digestChecksum, encoded.digestPayload, needDigest,
	)
}

func sqliteV4RollupRecordDataEqual(left, right sqliteV4RollupRecord) bool {
	return left.bucketNano == right.bucketNano && left.count == right.count && left.lossCount == right.lossCount &&
		left.sumBits == right.sumBits && left.sumSqBits == right.sumSqBits &&
		left.minBits == right.minBits && left.maxBits == right.maxBits &&
		left.firstBits == right.firstBits && left.firstTS == right.firstTS &&
		left.lastBits == right.lastBits && left.lastTS == right.lastTS &&
		sqliteV4TDigestsEqual(left.digest, right.digest)
}

func sqliteV4RollupRecordDataSlicesEqual(left, right []sqliteV4RollupRecord) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !sqliteV4RollupRecordDataEqual(left[i], right[i]) {
			return false
		}
	}
	return true
}
