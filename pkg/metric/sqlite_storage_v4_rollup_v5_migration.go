package metric

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

func (s *Store) migrateSQLiteV4SharedRollupBlocks(ctx context.Context) (int64, int64, int64, error) {
	var blockCount int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+s.tables.rollupBlocks+
		` WHERE axis_id IS NULL OR codec != ? OR digest_codec != ?`,
		sqliteV4SharedRollupBlockCodec, sqliteV4StructuredRollupDigestCodec,
	).Scan(&blockCount); err != nil {
		return 0, 0, 0, fmt.Errorf("metric: count SQLite V4 shared rollup migration: %w", err)
	}
	if blockCount == 0 {
		return 0, 0, 0, nil
	}
	s.reportMigrationProgress(MigrationPhaseUpgradingRollupBlocks, 0, blockCount, 0)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("metric: begin SQLite V4 shared rollup migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	type key struct {
		seriesID   int64
		resolution int64
		startNano  int64
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		`SELECT series_id, resolution_nano, start_nano FROM %s
		 WHERE axis_id IS NULL OR codec != ? OR digest_codec != ?
		 ORDER BY series_id, resolution_nano, start_nano`,
		s.tables.rollupBlocks,
	), sqliteV4SharedRollupBlockCodec, sqliteV4StructuredRollupDigestCodec)
	if err != nil {
		return 0, 0, 0, err
	}
	keys := make([]key, 0, blockCount)
	for rows.Next() {
		var item key
		if err := rows.Scan(&item.seriesID, &item.resolution, &item.startNano); err != nil {
			_ = rows.Close()
			return 0, 0, 0, err
		}
		keys = append(keys, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, 0, err
	}

	var migratedBlocks, migratedBuckets, skippedBlocks int64
	for index, item := range keys {
		var endNano, checksum, digestChecksum int64
		var count, codec, digestCodec int
		var payload, digestPayload []byte
		var axisID sql.NullInt64
		err := tx.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT end_nano, bucket_count, codec, checksum, payload,
			        digest_codec, digest_checksum, digest_payload, axis_id
			   FROM %s WHERE series_id = ? AND resolution_nano = ? AND start_nano = ?`,
			s.tables.rollupBlocks,
		), item.seriesID, item.resolution, item.startNano).Scan(
			&endNano, &count, &codec, &checksum, &payload,
			&digestCodec, &digestChecksum, &digestPayload, &axisID,
		)
		if err != nil {
			return migratedBlocks, migratedBuckets, skippedBlocks, err
		}
		records, decodeErr := s.decodeSQLiteV4RollupBlockFromStorage(
			ctx, tx, codec, count, uint32(checksum), payload, axisID,
			digestCodec, uint32(digestChecksum), digestPayload, true,
		)
		if decodeErr != nil {
			skippedBlocks++
			log.Printf("metric: preserving readable legacy rollup block after conversion failure series=%d resolution=%d start=%d: %v",
				item.seriesID, item.resolution, item.startNano, decodeErr)
			s.reportMigrationProgressWithDeferred(MigrationPhaseUpgradingRollupBlocks, int64(index+1), blockCount, migratedBuckets, skippedBlocks)
			continue
		}
		if len(records) == 0 || records[0].bucketNano != item.startNano || records[len(records)-1].bucketNano != endNano {
			skippedBlocks++
			log.Printf("metric: preserving legacy rollup block with boundary mismatch series=%d resolution=%d start=%d",
				item.seriesID, item.resolution, item.startNano)
			s.reportMigrationProgressWithDeferred(MigrationPhaseUpgradingRollupBlocks, int64(index+1), blockCount, migratedBuckets, skippedBlocks)
			continue
		}
		encoded, encodeErr := encodeSQLiteV4RollupBlock(records)
		if encodeErr != nil {
			return migratedBlocks, migratedBuckets, skippedBlocks, encodeErr
		}
		decoded, decodeErr := decodeSQLiteV4StoredRollupBlock(
			encoded.codec, encoded.count, encoded.checksum, encoded.payload,
			encoded.axisCodec, encoded.axisChecksum, encoded.axisPayload,
			encoded.digestCodec, encoded.digestChecksum, encoded.digestPayload, true,
		)
		if decodeErr != nil || !sqliteV4RollupRecordDataSlicesEqual(records, decoded) {
			if decodeErr == nil {
				decodeErr = fmt.Errorf("round-trip validation changed metric data")
			}
			return migratedBlocks, migratedBuckets, skippedBlocks,
				fmt.Errorf("metric: validate SQLite V4 shared rollup migration: %w", decodeErr)
		}
		newAxisID, err := s.storeSQLiteV4RollupAxisTx(ctx, tx, encoded)
		if err != nil {
			return migratedBlocks, migratedBuckets, skippedBlocks, err
		}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s
			   SET end_nano = ?, bucket_count = ?, codec = ?, checksum = ?, payload = ?,
			       digest_codec = ?, digest_checksum = ?, digest_payload = ?, axis_id = ?
			 WHERE series_id = ? AND resolution_nano = ? AND start_nano = ?`,
			s.tables.rollupBlocks,
		), encoded.endNano, encoded.count, encoded.codec, int64(encoded.checksum), encoded.payload,
			encoded.digestCodec, int64(encoded.digestChecksum), encoded.digestPayload, newAxisID,
			item.seriesID, item.resolution, item.startNano)
		if err != nil {
			return migratedBlocks, migratedBuckets, skippedBlocks, err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			if err == nil {
				err = fmt.Errorf("updated %d rows, want 1", affected)
			}
			return migratedBlocks, migratedBuckets, skippedBlocks, err
		}
		migratedBlocks++
		migratedBuckets += int64(len(records))
		s.reportMigrationProgressWithDeferred(MigrationPhaseUpgradingRollupBlocks, int64(index+1), blockCount, migratedBuckets, skippedBlocks)
	}
	if err := s.cleanupSQLiteV4RollupAxesTx(ctx, tx); err != nil {
		return migratedBlocks, migratedBuckets, skippedBlocks, err
	}
	if err := tx.Commit(); err != nil {
		return migratedBlocks, migratedBuckets, skippedBlocks, fmt.Errorf("metric: commit SQLite V4 shared rollup migration: %w", err)
	}
	return migratedBlocks, migratedBuckets, skippedBlocks, nil
}
