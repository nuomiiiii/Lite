package metric

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

var (
	digestEncoderOnce sync.Once
	digestEncoder     *zstd.Encoder
	digestEncoderErr  error
)

func getDigestEncoder() (*zstd.Encoder, error) {
	digestEncoderOnce.Do(func() {
		digestEncoder, digestEncoderErr = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(1)))
	})
	return digestEncoder, digestEncoderErr
}

// encodeStoredTDigest returns the upstream 1.4.x on-disk representation.
// Compression is per rollup: a poorly compressible digest is stored raw
// instead of growing larger.
func encodeStoredTDigest(t *TDigest) []byte {
	if t == nil {
		return nil
	}
	raw := t.encodeRaw()
	encoder, err := getDigestEncoder()
	if err != nil {
		return append([]byte(nil), raw...)
	}
	compressed := encoder.EncodeAll(raw, nil)
	if len(compressed)+storedDigestHeaderSize < len(raw) {
		out := make([]byte, storedDigestHeaderSize+len(compressed))
		out[0] = storedDigestMagic0
		out[1] = storedDigestTypeZstd
		out[2] = storedDigestVersion
		copy(out[storedDigestHeaderSize:], compressed)
		return out
	}
	out := make([]byte, storedDigestHeaderSize+len(raw))
	out[0] = storedDigestMagic0
	out[1] = storedDigestTypeRaw
	out[2] = storedDigestVersion
	copy(out[storedDigestHeaderSize:], raw)
	return out
}
