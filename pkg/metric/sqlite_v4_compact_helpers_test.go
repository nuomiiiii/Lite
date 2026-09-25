package metric

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

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
