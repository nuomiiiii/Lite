package metric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

func (s *Store) createSQLiteV4RollupAxes(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY,
		hash BLOB NOT NULL UNIQUE,
		codec INTEGER NOT NULL,
		checksum INTEGER NOT NULL,
		bucket_count INTEGER NOT NULL,
		payload BLOB NOT NULL,
		CHECK(bucket_count > 0)
	)`, s.tables.rollupAxes)); err != nil {
		return fmt.Errorf("metric: create SQLite V4 rollup axis table: %w", err)
	}
	return nil
}

func (s *Store) createSQLiteV4RollupAxisReferences(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_rollup_blocks_axis_idx ON %s (axis_id)`,
			s.cfg.TablePrefix, s.tables.rollupBlocks),
		fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s_rollup_axis_gc
			AFTER DELETE ON %s
			WHEN OLD.axis_id IS NOT NULL
			BEGIN
				DELETE FROM %s
				WHERE id = OLD.axis_id
				  AND NOT EXISTS (SELECT 1 FROM %s WHERE axis_id = OLD.axis_id);
			END`, s.cfg.TablePrefix, s.tables.rollupBlocks, s.tables.rollupAxes, s.tables.rollupBlocks),
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metric: create SQLite V4 rollup axis reference: %w", err)
		}
	}
	return nil
}

func (s *Store) storeSQLiteV4RollupAxisTx(ctx context.Context, tx *sql.Tx, encoded sqliteV4EncodedRollupBlock) (int64, error) {
	if encoded.axisCodec == 0 || len(encoded.axisPayload) == 0 {
		return 0, fmt.Errorf("metric: cannot store an empty SQLite V4 rollup axis")
	}
	hash := sha256.Sum256(encoded.axisPayload)
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT OR IGNORE INTO %s (hash, codec, checksum, bucket_count, payload) VALUES (?, ?, ?, ?, ?)`,
		s.tables.rollupAxes,
	), hash[:], encoded.axisCodec, int64(encoded.axisChecksum), encoded.count, encoded.axisPayload); err != nil {
		return 0, fmt.Errorf("metric: store SQLite V4 rollup axis: %w", err)
	}
	var id, checksum int64
	var codec, bucketCount int
	var payload []byte
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT id, codec, checksum, bucket_count, payload FROM %s WHERE hash = ?`,
		s.tables.rollupAxes,
	), hash[:]).Scan(&id, &codec, &checksum, &bucketCount, &payload); err != nil {
		return 0, fmt.Errorf("metric: load SQLite V4 rollup axis: %w", err)
	}
	if codec != encoded.axisCodec || uint32(checksum) != encoded.axisChecksum ||
		bucketCount != encoded.count || !bytes.Equal(payload, encoded.axisPayload) {
		return 0, fmt.Errorf("metric: SQLite V4 rollup axis hash collision")
	}
	return id, nil
}

func (s *Store) loadSQLiteV4RollupAxis(ctx context.Context, q querier, axisID sql.NullInt64) (int, uint32, []byte, error) {
	if !axisID.Valid || axisID.Int64 <= 0 {
		return 0, 0, nil, nil
	}
	var codec int
	var checksum int64
	var payload []byte
	if err := q.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT codec, checksum, payload FROM %s WHERE id = ?`, s.tables.rollupAxes,
	), axisID.Int64).Scan(&codec, &checksum, &payload); err != nil {
		return 0, 0, nil, fmt.Errorf("metric: load SQLite V4 rollup axis %d: %w", axisID.Int64, err)
	}
	return codec, uint32(checksum), payload, nil
}

func (s *Store) cleanupSQLiteV4RollupAxesTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE NOT EXISTS (SELECT 1 FROM %s WHERE axis_id = %s.id)`,
		s.tables.rollupAxes, s.tables.rollupBlocks, s.tables.rollupAxes,
	)); err != nil {
		return fmt.Errorf("metric: clean orphaned SQLite V4 rollup axes: %w", err)
	}
	return nil
}

func decodeSQLiteV4RollupBlockWithAxisReference(
	codec, count int,
	checksum uint32,
	payload []byte,
	axisCodec, axisChecksum sql.NullInt64,
	axisPayload []byte,
	digestCodec int,
	digestChecksum uint32,
	digestPayload []byte,
	needDigest bool,
) ([]sqliteV4RollupRecord, error) {
	var storedAxisCodec int
	var storedAxisChecksum uint32
	if axisCodec.Valid {
		storedAxisCodec = int(axisCodec.Int64)
	}
	if axisChecksum.Valid {
		storedAxisChecksum = uint32(axisChecksum.Int64)
	}
	return decodeSQLiteV4StoredRollupBlock(
		codec, count, checksum, payload,
		storedAxisCodec, storedAxisChecksum, axisPayload,
		digestCodec, digestChecksum, digestPayload, needDigest,
	)
}

func (s *Store) decodeSQLiteV4RollupBlockFromStorage(
	ctx context.Context,
	q querier,
	codec, count int,
	checksum uint32,
	payload []byte,
	axisID sql.NullInt64,
	digestCodec int,
	digestChecksum uint32,
	digestPayload []byte,
	needDigest bool,
) ([]sqliteV4RollupRecord, error) {
	axisCodec, axisChecksum, axisPayload, err := s.loadSQLiteV4RollupAxis(ctx, q, axisID)
	if err != nil {
		return nil, err
	}
	return decodeSQLiteV4StoredRollupBlock(codec, count, checksum, payload,
		axisCodec, axisChecksum, axisPayload,
		digestCodec, digestChecksum, digestPayload, needDigest)
}
