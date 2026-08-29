package metric

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Persisted digest blobs have a small envelope so the legacy TD encoding can
// remain readable while newly written rows use compression when it actually
// saves space. TU is an uncompressed new-format payload and TZ is a
// compressed payload; both contain the original TD bytes after the envelope.
//
// Upstream 1.4.x stores TZ as zstd. Lite historically stored TZ as raw
// DEFLATE. Decode accepts both so a 1.4.3 metrics.db can be imported without
// rewriting the fork's own already-written flate rows.
const (
	storedDigestMagic0     = 'T'
	storedDigestVersion    = 1
	storedDigestTypeZstd   = 'Z'
	storedDigestTypeRaw    = 'U'
	storedDigestHeaderSize = 3
)

var (
	digestEncoderOnce sync.Once
	digestEncoder     *zstd.Encoder
	digestEncoderErr  error
	digestDecoderOnce sync.Once
	digestDecoder     *zstd.Decoder
	digestDecoderErr  error
)

func getDigestEncoder() (*zstd.Encoder, error) {
	digestEncoderOnce.Do(func() {
		digestEncoder, digestEncoderErr = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(1)))
	})
	return digestEncoder, digestEncoderErr
}

func getDigestDecoder() (*zstd.Decoder, error) {
	digestDecoderOnce.Do(func() {
		digestDecoder, digestDecoderErr = zstd.NewReader(nil)
	})
	return digestDecoder, digestDecoderErr
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

// decodeStoredTDigest unwraps upstream 1.4.x TZ/TU envelopes, Lite's flate TZ
// blobs, and the legacy raw TD blob.
func decodeStoredTDigest(blob []byte) ([]byte, error) {
	if len(blob) < storedDigestHeaderSize || blob[0] != storedDigestMagic0 {
		return blob, nil
	}
	switch blob[1] {
	case tdigestMagic1:
		return blob, nil
	case storedDigestTypeRaw:
		if blob[2] != storedDigestVersion {
			return nil, fmt.Errorf("metric: unsupported stored t-digest version %d", blob[2])
		}
		return blob[storedDigestHeaderSize:], nil
	case storedDigestTypeZstd:
		if blob[2] != storedDigestVersion {
			return nil, errors.New("metric: unsupported compressed t-digest version")
		}
		if decoder, err := getDigestDecoder(); err == nil {
			if raw, err := decoder.DecodeAll(blob[storedDigestHeaderSize:], nil); err == nil {
				return raw, nil
			}
		}
		reader := flate.NewReader(bytes.NewReader(blob[storedDigestHeaderSize:]))
		decompressed, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return nil, errors.New("metric: invalid compressed t-digest blob")
		}
		if len(decompressed) > 64<<20 {
			return nil, errors.New("metric: compressed t-digest blob is too large")
		}
		return decompressed, nil
	default:
		return nil, fmt.Errorf("metric: invalid stored t-digest type %q", blob[1])
	}
}
