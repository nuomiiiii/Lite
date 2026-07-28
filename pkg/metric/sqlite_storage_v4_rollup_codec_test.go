package metric

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"testing"
	"time"
)

func TestSQLiteV4RollupCodecRoundTripPreservesBits(t *testing.T) {
	digest := NewTDigest(100)
	for i := 0; i < 200; i++ {
		digest.Add(float64(i)/7, 1)
	}
	digestBlob := digest.Encode()
	records := make([]sqliteV4RollupRecord, 700)
	for i := range records {
		bucket := int64(-9_000_000_000 + i*60_000_000_000)
		records[i] = sqliteV4RollupRecord{
			bucketNano: bucket,
			count:      int64(10 + i%7),
			sumBits:    math.Float64bits(float64(i)*1.25 + 100),
			sumSqBits:  math.Float64bits(float64(i*i) + 0.5),
			minBits:    math.Float64bits(float64(i) - 7.5),
			maxBits:    math.Float64bits(float64(i) + 9.5),
			firstBits:  math.Float64bits(float64(i) + 0.125),
			firstTS:    bucket + int64(i%11),
			lastBits:   math.Float64bits(float64(i) + 0.875),
			lastTS:     bucket + 59_999_999_999 - int64(i%13),
			digest:     digestBlob,
			createdAt:  1_700_000_000_000_000_000 + int64(i*17),
		}
	}
	for start := 0; start < len(records); start += sqliteV4RollupBlockLimit {
		end := min(start+sqliteV4RollupBlockLimit, len(records))
		encoded, err := encodeSQLiteV4RollupBlock(records[start:end])
		if err != nil {
			t.Fatal(err)
		}
		if encoded.digestCodec != sqliteV4StructuredRollupDigestCodec {
			t.Fatalf("digest codec=%d, want %d", encoded.digestCodec, sqliteV4StructuredRollupDigestCodec)
		}
		decoded, err := decodeSQLiteV4EncodedRollupBlock(encoded, true)
		if err != nil {
			t.Fatal(err)
		}
		if !sqliteV4RollupRecordDataSlicesEqual(records[start:end], decoded) {
			t.Fatal("SQLite V4 rollup codec changed a float bit pattern, timestamp, count, digest, or creation time")
		}
	}
}

func TestSQLiteV4DenseDigestPreservesMixedCompressionAndUnusualMeans(t *testing.T) {
	makeDigest := func(compression float64, values ...float64) []byte {
		digest := NewTDigest(compression)
		for _, value := range values {
			digest.Add(value, 1)
		}
		return digest.Encode()
	}
	unordered := NewTDigest(100)
	unordered.processed = true
	unordered.min = 1
	unordered.max = 2
	unordered.count = 2
	unordered.centroids = []centroid{{mean: 2, weight: 1}, {mean: 1, weight: 1}}
	digests := [][]byte{
		makeDigest(100, -20, -3.5, -0.0, 0, 4.25, 19),
		makeDigest(200, -1000, -1, 1, 1000),
		unordered.encodeRaw(),
		nil,
	}
	records := make([]sqliteV4RollupRecord, len(digests))
	for index, digestBlob := range digests {
		digest, err := DecodeTDigest(digestBlob)
		if err != nil {
			t.Fatal(err)
		}
		count := int64(digest.Count())
		minValue, maxValue := digest.min, digest.max
		if len(digestBlob) == 0 {
			count, minValue, maxValue = 1, float64(index), float64(index)
		}
		records[index] = sqliteV4RollupRecord{
			bucketNano: int64(index+1) * time.Minute.Nanoseconds(),
			count:      count,
			sumBits:    math.Float64bits(float64(index) + 1), sumSqBits: math.Float64bits(float64(index) + 2),
			minBits: math.Float64bits(minValue), maxBits: math.Float64bits(maxValue),
			firstBits: math.Float64bits(minValue), firstTS: int64(index+1) * time.Minute.Nanoseconds(),
			lastBits: math.Float64bits(maxValue), lastTS: int64(index+1)*time.Minute.Nanoseconds() + 1,
			digest: digestBlob, createdAt: int64(index+1)*time.Minute.Nanoseconds() + 2,
		}
	}
	encoded, err := encodeSQLiteV4RollupBlock(records)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSQLiteV4EncodedRollupBlock(encoded, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sqliteV4RollupRecordDataSlicesEqual(records, decoded) {
		t.Fatal("dense SQLite V4 digest codec changed mixed or unusual digest bits")
	}
}

func TestSQLiteV4CompactDigestCodec2RemainsReadable(t *testing.T) {
	digest := NewTDigest(100)
	for index := 0; index < 200; index++ {
		digest.Add(float64(index)/9, 1)
	}
	records := []sqliteV4RollupRecord{{
		bucketNano: time.Minute.Nanoseconds(), count: 200,
		sumBits: math.Float64bits(1), sumSqBits: math.Float64bits(2),
		minBits: math.Float64bits(digest.min), maxBits: math.Float64bits(digest.max),
		firstBits: math.Float64bits(digest.min), firstTS: time.Minute.Nanoseconds(),
		lastBits: math.Float64bits(digest.max), lastTS: time.Minute.Nanoseconds() + 1,
		digest: digest.Encode(), createdAt: time.Minute.Nanoseconds() + 2,
	}}
	encoded, err := encodeSQLiteV4RollupBlockV2(records)
	if err != nil {
		t.Fatal(err)
	}
	var legacy bytes.Buffer
	legacy.WriteString(sqliteV4RollupDigestCompactMagic)
	appendUvarintTo(&legacy, uint64(len(records)))
	for _, record := range records {
		compact, err := encodeSQLiteV4CompactTDigest(record)
		if err != nil {
			t.Fatal(err)
		}
		appendUvarintTo(&legacy, uint64(len(compact)))
		legacy.Write(compact)
	}
	legacyPayload, err := compressSQLiteV4RollupSection(legacy.Bytes(), sqliteV4RollupDigestLevel)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSQLiteV4RollupBlock(encoded.codec, encoded.count, encoded.checksum, encoded.payload,
		sqliteV4CompactRollupDigestCodec, crc32.ChecksumIEEE(legacyPayload), legacyPayload, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sqliteV4RollupRecordDataSlicesEqual(records, decoded) {
		t.Fatal("legacy compact digest codec changed while retaining read compatibility")
	}
}

func TestSQLiteV4RollupCodecRejectsCorruption(t *testing.T) {
	digest := NewTDigest(100)
	digest.Add(1, 1)
	record := sqliteV4RollupRecord{
		bucketNano: 1, count: 1,
		sumBits: math.Float64bits(1), sumSqBits: math.Float64bits(1),
		minBits: math.Float64bits(1), maxBits: math.Float64bits(1),
		firstBits: math.Float64bits(1), firstTS: 1,
		lastBits: math.Float64bits(1), lastTS: 1,
		digest: digest.Encode(), createdAt: 2,
	}
	encoded, err := encodeSQLiteV4RollupBlock([]sqliteV4RollupRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	encoded.payload[len(encoded.payload)-1] ^= 0xff
	if _, err := decodeSQLiteV4EncodedRollupBlock(encoded, true); err == nil {
		t.Fatal("corrupt SQLite V4 rollup block unexpectedly decoded")
	}
}

func TestSQLiteV4RollupSummaryDecodeDoesNotReadDigestSection(t *testing.T) {
	digest := NewTDigest(100)
	for i := 0; i < 1000; i++ {
		digest.Add(float64(i%137), 1)
	}
	record := sqliteV4RollupRecord{
		bucketNano: 1, count: 1000,
		sumBits: math.Float64bits(1), sumSqBits: math.Float64bits(2),
		minBits: math.Float64bits(0), maxBits: math.Float64bits(136),
		firstBits: math.Float64bits(1), firstTS: 1,
		lastBits: math.Float64bits(2), lastTS: 2,
		digest: digest.Encode(), createdAt: 3,
	}
	encoded, err := encodeSQLiteV4RollupBlock([]sqliteV4RollupRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	encoded.digestPayload[len(encoded.digestPayload)-1] ^= 0xff
	decoded, err := decodeSQLiteV4EncodedRollupBlock(encoded, false)
	if err != nil || len(decoded) != 1 || len(decoded[0].digest) != 0 {
		t.Fatalf("summary-only decode touched digest section: records=%d err=%v", len(decoded), err)
	}
	if _, err := decodeSQLiteV4EncodedRollupBlock(encoded, true); err == nil {
		t.Fatal("percentile decode unexpectedly accepted a corrupt digest section")
	}
}

func TestSQLiteV4CompactDigestFallsBackForNonExactWeights(t *testing.T) {
	digest := NewTDigest(100000)
	digest.Add(10.25, 0.5)
	digest.Add(20.5, 1.25)
	digest.Add(30.75, math.Ldexp(1, 64))
	record := sqliteV4RollupRecord{
		count:   -1,
		minBits: math.Float64bits(digest.min),
		maxBits: math.Float64bits(digest.max),
		digest:  digest.Encode(),
	}
	compact, err := encodeSQLiteV4CompactTDigest(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) < 9 {
		t.Fatalf("compact digest length=%d, want header", len(compact))
	}
	if binary.LittleEndian.Uint64(compact[0:8]) != math.Float64bits(digest.compression) {
		t.Fatal("compact digest changed compression")
	}
	if compact[8]&sqliteV4DigestIntegerWeights != 0 {
		t.Fatal("non-exact weights unexpectedly used integer encoding")
	}
	decoded, err := decodeSQLiteV4CompactTDigest(compact, record)
	if err != nil {
		t.Fatal(err)
	}
	if !sqliteV4TDigestsEqual(record.digest, decoded) {
		t.Fatal("compact digest changed non-integral or large weight bits")
	}
}
