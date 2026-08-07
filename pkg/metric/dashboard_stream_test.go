package metric

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"hash/crc32"
	"math"
	"reflect"
	"testing"
	"time"
)

type dashboardPayloadFixture struct {
	name    string
	payload []byte
}

func dashboardPayloadFixtures(t *testing.T, payload []byte) []dashboardPayloadFixture {
	t.Helper()
	raw, err := inflateSQLiteV4Payload(payload)
	if err != nil {
		t.Fatal(err)
	}
	rawPayload := append([]byte{sqliteV4PayloadRaw}, raw...)
	fixtures := []dashboardPayloadFixture{{name: "raw", payload: rawPayload}}
	if payload[0] == sqliteV4PayloadDeflate {
		fixtures = append(fixtures, dashboardPayloadFixture{name: "deflate", payload: append([]byte(nil), payload...)})
	}
	return fixtures
}

func TestDashboardPointVisitorsMatchFullDecoders(t *testing.T) {
	base := time.Date(2026, 8, 7, 8, 0, 0, 123, time.UTC).UnixNano()
	points := make([]sqliteV4BlockPoint, 2048)
	for index := range points {
		value := float64(index%17) / 4
		points[index] = sqliteV4BlockPoint{
			timestamp: base + int64(index)*15*time.Second.Nanoseconds(),
			valueBits: math.Float64bits(value),
			labels:    []string{`{}`, `{"source":"agent"}`}[index%2],
			createdAt: base + int64(index/10),
		}
	}
	points[0].valueBits = math.Float64bits(math.Copysign(0, -1))
	points[1].valueBits = math.Float64bits(math.SmallestNonzeroFloat64)
	points[2].valueBits = 0x7ff8000000001234

	legacy, err := encodeSQLiteV4Block(points)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := encodeSQLiteV6SharedPointBlock(points)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.payload[0] != sqliteV4PayloadDeflate || shared.payload[0] != sqliteV4PayloadDeflate {
		t.Fatalf("point fixtures must exercise deflate: v4=%d v6=%d", legacy.payload[0], shared.payload[0])
	}

	store := &Store{}
	ctx := withDashboardAxisQueryCache(context.Background())
	for _, fixture := range []struct {
		name    string
		encoded sqliteV4EncodedBlock
	}{
		{name: "v4", encoded: legacy},
		{name: "v6", encoded: shared},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			want, err := decodeSQLiteStoredPointBlock(
				fixture.encoded.codec, fixture.encoded.count, fixture.encoded.checksum, fixture.encoded.payload,
				fixture.encoded.axisCodec, fixture.encoded.axisChecksum, fixture.encoded.axisPayload,
			)
			if err != nil {
				t.Fatal(err)
			}
			for _, payloadFixture := range dashboardPayloadFixtures(t, fixture.encoded.payload) {
				payloadFixture := payloadFixture
				t.Run(payloadFixture.name, func(t *testing.T) {
					axisID := sql.NullInt64{}
					if fixture.encoded.codec == sqliteV6SharedPointBlockCodec {
						axisID = sql.NullInt64{Int64: 101, Valid: true}
					}
					for pass := 0; pass < 2; pass++ {
						got := make([]sqliteV4BlockPoint, 0, len(want))
						first, last, err := store.visitSQLiteDashboardPointBlock(ctx,
							fixture.encoded.codec, fixture.encoded.count, crc32.ChecksumIEEE(payloadFixture.payload), payloadFixture.payload,
							axisID,
							sql.NullInt64{Int64: int64(fixture.encoded.axisCodec), Valid: fixture.encoded.axisCodec != 0},
							sql.NullInt64{Int64: int64(fixture.encoded.axisChecksum), Valid: fixture.encoded.axisCodec != 0},
							fixture.encoded.axisPayload,
							func(timestamp int64, valueBits uint64) error {
								got = append(got, sqliteV4BlockPoint{timestamp: timestamp, valueBits: valueBits})
								return nil
							},
						)
						if err != nil {
							t.Fatal(err)
						}
						if first != want[0].timestamp || last != want[len(want)-1].timestamp || len(got) != len(want) {
							t.Fatalf("boundary/count mismatch first=%d last=%d count=%d", first, last, len(got))
						}
						for index := range want {
							if got[index].timestamp != want[index].timestamp || got[index].valueBits != want[index].valueBits {
								t.Fatalf("point %d changed: got=%#v want=%#v", index, got[index], want[index])
							}
						}
					}
				})
			}
		})
	}
}

func TestDashboardSharedRollupVisitorMatchesFullDecoder(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		integer   bool
		makeValue func(int, int) float64
	}{
		{name: "integer", integer: true, makeValue: func(index, field int) float64 {
			return float64(index*7 + field + 1)
		}},
		{name: "raw", makeValue: func(index, field int) float64 {
			if index == 0 {
				return math.Float64frombits(0x7ff8000000000001 + uint64(field))
			}
			return math.Sin(float64(index*11+field)) * math.Pi
		}},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			records := make([]sqliteV4RollupRecord, 256)
			base := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC).UnixNano()
			for index := range records {
				bucket := base + int64(index)*time.Minute.Nanoseconds()
				values := [sqliteV4RollupFloatFieldCount]uint64{}
				for field := range values {
					values[field] = math.Float64bits(fixture.makeValue(index, field))
				}
				records[index] = sqliteV4RollupRecord{
					bucketNano: bucket, count: int64(index%9 + 1), lossCount: int64(index % 2),
					sumBits: values[0], sumSqBits: values[1], minBits: values[2], maxBits: values[3],
					firstBits: values[4], firstTS: bucket + int64(index%7),
					lastBits: values[5], lastTS: bucket + time.Minute.Nanoseconds() - 1 - int64(index%5),
					createdAt: base + int64(index),
				}
			}
			encoded, err := encodeSQLiteV4RollupBlock(records)
			if err != nil {
				t.Fatal(err)
			}
			want, err := decodeSQLiteV4EncodedRollupBlock(encoded, false)
			if err != nil {
				t.Fatal(err)
			}

			raw, err := inflateSQLiteV4Payload(encoded.payload)
			if err != nil {
				t.Fatal(err)
			}
			reader := bytes.NewReader(raw)
			if err := readSQLiteDashboardMagic(reader, sqliteV4RollupValueMagic); err != nil {
				t.Fatal(err)
			}
			if _, err := binary.ReadUvarint(reader); err != nil {
				t.Fatal(err)
			}
			for field := 0; field < sqliteV4RollupFloatFieldCount; field++ {
				iterator, err := newSQLiteDashboardLosslessIterator(reader, raw, len(records))
				if err != nil {
					t.Fatal(err)
				}
				if iterator.integer != fixture.integer {
					t.Fatalf("field %d integer=%v, want %v", field, iterator.integer, fixture.integer)
				}
			}

			store := &Store{}
			ctx := withDashboardAxisQueryCache(context.Background())
			for _, payloadFixture := range dashboardPayloadFixtures(t, encoded.payload) {
				for pass := 0; pass < 2; pass++ {
					got := make([]sqliteV4RollupRecord, 0, len(want))
					first, last, err := store.visitSQLiteDashboardRollupBlock(ctx,
						encoded.codec, encoded.count, crc32.ChecksumIEEE(payloadFixture.payload), payloadFixture.payload,
						sql.NullInt64{Int64: 202, Valid: true},
						sql.NullInt64{Int64: int64(encoded.axisCodec), Valid: true},
						sql.NullInt64{Int64: int64(encoded.axisChecksum), Valid: true}, encoded.axisPayload,
						func(record sqliteV4RollupRecord) error {
							got = append(got, record)
							return nil
						},
					)
					if err != nil {
						t.Fatal(err)
					}
					if first != encoded.startNano || last != encoded.endNano || !reflect.DeepEqual(got, want) {
						t.Fatalf("streamed rollup changed data: first=%d last=%d count=%d", first, last, len(got))
					}
				}
			}
		})
	}
}
