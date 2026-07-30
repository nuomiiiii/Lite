package metric

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func encodeLegacyStructuredDigestFixture(records []sqliteV4RollupRecord) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString(sqliteV4LegacyStructuredDigestMagic)
	appendUvarintTo(&raw, uint64(len(records)))
	raw.WriteByte(0) // Store compression per digest to keep the fixture simple.
	var encoded [8]byte
	for _, record := range records {
		if len(record.digest) == 0 {
			raw.WriteByte(0)
			continue
		}
		digest, err := sqliteV4RawTDigest(record.digest)
		if err != nil {
			return nil, err
		}
		if len(digest) < 39 || digest[0] != tdigestMagic0 || digest[1] != tdigestMagic1 || digest[2] != tdigestVersion {
			return nil, fmt.Errorf("invalid t-digest fixture header")
		}
		centroids := int(binary.LittleEndian.Uint32(digest[35:39]))
		if len(digest) != 39+centroids*16 {
			return nil, fmt.Errorf("invalid t-digest fixture length")
		}
		raw.WriteByte(byte(sqliteV4StructuredDigestPresent))
		for _, offset := range []int{3, 11, 19, 27} {
			raw.Write(digest[offset : offset+8])
		}
		appendUvarintTo(&raw, uint64(centroids))
		if centroids > 0 {
			first := binary.LittleEndian.Uint64(digest[39:47])
			binary.LittleEndian.PutUint64(encoded[:], sqliteV4OrderedFloatBits(first))
			raw.Write(encoded[:])
			previous := first
			for index := 1; index < centroids; index++ {
				offset := 39 + index*16
				mean := binary.LittleEndian.Uint64(digest[offset : offset+8])
				appendUvarintTo(&raw, sqliteV4OrderedFloatBits(mean)-sqliteV4OrderedFloatBits(previous))
				previous = mean
			}
		}
		for index := 0; index < centroids; index++ {
			offset := 39 + index*16 + 8
			raw.Write(digest[offset : offset+8])
		}
	}
	return compressSQLiteV4RollupSection(raw.Bytes(), sqliteV4RollupDigestLevel)
}

func v9DigestFixtureRecords(base time.Time) []sqliteV4RollupRecord {
	records := make([]sqliteV4RollupRecord, 3)
	for index := range records {
		value := 10.25 + float64(index)*3.5
		digest := NewTDigest(defaultTDigestCompression)
		digest.Add(value, 1)
		stamp := base.Add(time.Duration(index) * time.Minute).UnixNano()
		records[index] = sqliteV4RollupRecord{
			bucketNano: stamp,
			count:      1,
			sumBits:    math.Float64bits(value),
			sumSqBits:  math.Float64bits(value * value),
			minBits:    math.Float64bits(value),
			maxBits:    math.Float64bits(value),
			firstBits:  math.Float64bits(value),
			firstTS:    stamp,
			lastBits:   math.Float64bits(value),
			lastTS:     stamp,
			digest:     digest.Encode(),
			createdAt:  stamp,
		}
	}
	return records
}

func TestSQLiteV9ReadsLegacyStructuredDigestCodec(t *testing.T) {
	records := v9DigestFixtureRecords(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC))
	payload, err := encodeLegacyStructuredDigestFixture(records)
	if err != nil {
		t.Fatal(err)
	}
	decoded := append([]sqliteV4RollupRecord(nil), records...)
	for index := range decoded {
		decoded[index].digest = nil
	}
	if err := decodeSQLiteV4StructuredRollupDigests(decoded, sqliteV4LegacyStructuredDigestCodec, crc32.ChecksumIEEE(payload), payload); err != nil {
		t.Fatal(err)
	}
	if !sqliteV4RollupRecordDataSlicesEqual(records, decoded) {
		t.Fatal("legacy structured digest codec changed metric data")
	}
}

func seedSQLiteV8DigestStore(t *testing.T, dsn string, corrupt bool) []sqliteV4RollupRecord {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, SQLite(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateMetric(ctx, Definition{Name: "v9.digest", Type: TypeGauge, RetentionDays: 15}); err != nil {
		t.Fatal(err)
	}
	records := v9DigestFixtureRecords(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC))
	tagsHash, tagsJSON, err := tagsFingerprint(map[string]string{"source": "v8"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO metric_series (metric_name, entity_id, tags_hash, tags) VALUES (?, ?, ?, ?)`,
		"v9.digest", "node-a", tagsHash, tagsJSON); err != nil {
		t.Fatal(err)
	}
	var seriesID int64
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM metric_series WHERE metric_name = ? AND entity_id = ? AND tags_hash = ?`,
		"v9.digest", "node-a", tagsHash).Scan(&seriesID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeSQLiteV4RollupBlocksTx(ctx, tx, seriesID, time.Minute.Nanoseconds(), records); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	legacyPayload, err := encodeLegacyStructuredDigestFixture(records)
	if err != nil {
		t.Fatal(err)
	}
	if corrupt {
		legacyPayload = append([]byte(nil), legacyPayload...)
		legacyPayload[len(legacyPayload)/2] ^= 0x5a
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE metric_rollup_blocks SET digest_codec = ?, digest_checksum = ?, digest_payload = ? WHERE series_id = ?`,
		sqliteV4LegacyStructuredDigestCodec, int64(crc32.ChecksumIEEE(legacyPayload)), legacyPayload, seriesID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 8`); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestSQLiteV9MigratesDigestCodecLosslessly(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	want := seedSQLiteV8DigestStore(t, dsn, false)
	summary, err := InspectSQLiteMigration(ctx, SQLite(dsn))
	if err != nil || !summary.Required || summary.Layout != "v4" {
		t.Fatalf("V8 digest store did not enter migration page: summary=%#v err=%v", summary, err)
	}
	var phases []string
	store, err := Open(ctx, SQLite(dsn, WithMigrationProgress(func(progress MigrationProgress) {
		phases = append(phases, progress.Phase)
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !containsString(phases, MigrationPhaseEncodingDigests) {
		t.Fatalf("digest conversion was not visible in migration progress: %v", phases)
	}
	var codec, userVersion int
	if err := store.db.QueryRowContext(ctx, `SELECT digest_codec FROM metric_rollup_blocks LIMIT 1`).Scan(&codec); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if codec != sqliteV4StructuredRollupDigestCodec || userVersion != sqliteStorageVersionCurrent {
		t.Fatalf("migrated codec=%d version=%d", codec, userVersion)
	}
	series, err := store.sqliteV4MatchingSeries(ctx, store.db, "v9.digest", "node-a", map[string]string{"source": "v8"})
	if err != nil || len(series) != 1 {
		t.Fatalf("find migrated series: count=%d err=%v", len(series), err)
	}
	got, err := store.loadAllSQLiteV4RollupBlockRecords(ctx, store.db, series[0].id, time.Minute.Nanoseconds())
	if err != nil || !sqliteV4RollupRecordDataSlicesEqual(want, got) {
		t.Fatalf("V9 migration changed digest data: count=%d err=%v", len(got), err)
	}
}

func TestSQLiteV9MigrationFailureKeepsV8Blocks(t *testing.T) {
	ctx := context.Background()
	dsn := sqliteFileDSN(filepath.Join(t.TempDir(), "metrics.db"))
	seedSQLiteV8DigestStore(t, dsn, true)
	if store, err := Open(ctx, SQLite(dsn)); err == nil {
		_ = store.Close()
		t.Fatal("corrupt V8 digest unexpectedly migrated")
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var codec, userVersion int
	if err := db.QueryRowContext(ctx, `SELECT digest_codec FROM metric_rollup_blocks LIMIT 1`).Scan(&codec); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if codec != sqliteV4LegacyStructuredDigestCodec || userVersion != sqliteStorageVersionPingMerge {
		t.Fatalf("failed migration changed source codec=%d version=%d", codec, userVersion)
	}
}
