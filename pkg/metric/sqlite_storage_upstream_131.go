package metric

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

const upstream131CopyBatchSize = 5000

var upstream131DefinitionColumns = []string{
	"name", "type", "unit", "description", "retention_days", "metadata",
	"created_at_milli", "updated_at_milli",
}

var upstream131SeriesColumns = []string{"id", "metric_name", "entity_id", "tags_hash", "tags"}
var upstream131LabelColumns = []string{"id", "labels_hash", "labels"}
var upstream131ResolutionColumns = []string{"id", "resolution_milli"}
var upstream131RollupColumns = []string{
	"series_id", "resolution_id", "label_id", "bucket_milli", "count", "sum",
	"sum_sq", "min_val", "max_val", "first_val", "first_ts_milli", "last_val",
	"last_ts_milli", "digest", "created_at_milli",
}

// inspectSQLiteUpstream131Schema recognizes the normalized, rollup-only
// SQLite layout shipped by upstream 1.3.1. It checks every required table and
// column so an unrelated partial schema is never selected for conversion.
func inspectSQLiteUpstream131Schema(ctx context.Context, db *sql.DB, t tables) (bool, error) {
	required := []struct {
		name    string
		columns []string
	}{
		{t.definitions, upstream131DefinitionColumns},
		{t.series, upstream131SeriesColumns},
		{t.labels, upstream131LabelColumns},
		{t.resolutions, upstream131ResolutionColumns},
		{t.rollups, upstream131RollupColumns},
	}
	for _, item := range required {
		kind, err := sqliteObjectTypeDB(ctx, db, item.name)
		if err != nil {
			return false, err
		}
		if kind != "table" {
			return false, nil
		}
		columns, err := sqliteColumns(ctx, db, item.name)
		if err != nil {
			return false, fmt.Errorf("metric: inspect upstream 1.3.1 table %s: %w", item.name, err)
		}
		for _, column := range item.columns {
			if !columns[column] {
				return false, nil
			}
		}
	}
	return true, nil
}

type upstream131SourceTables struct {
	definitions string
	series      string
	labels      string
	resolutions string
	rollups     string
}

func (s *Store) upstream131SourceTables() upstream131SourceTables {
	return upstream131SourceTables{
		definitions: s.tables.definitions + "_upstream_131_source",
		series:      s.tables.series + "_upstream_131_source",
		labels:      s.tables.labels + "_upstream_131_source",
		resolutions: s.tables.resolutions + "_upstream_131_source",
		rollups:     s.tables.rollups + "_upstream_131_source",
	}
}

// migrateSQLiteUpstream131Storage converts upstream 1.3.1's millisecond,
// rollup-only schema into the fork's V3 staging layout. V4 encoding then runs
// through the existing validated path. Every rename and copy below is in one
// SQLite transaction, so an error leaves the original 1.3.1 tables intact.
func (s *Store) migrateSQLiteUpstream131Storage(ctx context.Context) error {
	matched, err := inspectSQLiteUpstream131Schema(ctx, s.db, s.tables)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("metric: SQLite database is not a complete upstream 1.3.1 metric store")
	}

	source := s.upstream131SourceTables()
	for _, name := range []string{source.definitions, source.series, source.labels, source.resolutions, source.rollups} {
		kind, err := sqliteObjectType(ctx, s.db, name)
		if err != nil {
			return err
		}
		if kind != "" {
			return fmt.Errorf("metric: upstream 1.3.1 migration staging object %s already exists", name)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metric: begin upstream 1.3.1 SQLite migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	renames := [][2]string{
		{s.tables.rollups, source.rollups},
		{s.tables.series, source.series},
		{s.tables.labels, source.labels},
		{s.tables.resolutions, source.resolutions},
		{s.tables.definitions, source.definitions},
	}
	for _, pair := range renames {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+pair[0]+` RENAME TO `+pair[1]); err != nil {
			return fmt.Errorf("metric: stage upstream 1.3.1 table %s: %w", pair[0], err)
		}
	}

	if err := s.createSQLiteCoreTables(ctx, tx); err != nil {
		return err
	}
	if err := s.createSQLiteV3PhysicalTables(ctx, tx); err != nil {
		return err
	}
	if err := s.copyUpstream131DefinitionsTx(ctx, tx, source.definitions); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id, metric_name, entity_id, tags_hash, tags)
		 SELECT id, metric_name, entity_id, tags_hash, tags FROM %s`,
		s.tables.series, source.series,
	)); err != nil {
		return fmt.Errorf("metric: copy upstream 1.3.1 series: %w", err)
	}

	var sourceRows int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+source.rollups).Scan(&sourceRows); err != nil {
		return fmt.Errorf("metric: count upstream 1.3.1 rollups: %w", err)
	}
	s.reportMigrationProgress(MigrationPhaseNormalizingRollups, 0, sourceRows, 0)
	copiedRows, targetRows, err := s.copyUpstream131RollupsTx(ctx, tx, source, sourceRows)
	if err != nil {
		return err
	}
	if copiedRows != sourceRows {
		return fmt.Errorf("metric: upstream 1.3.1 rollup count mismatch: source=%d copied=%d", sourceRows, copiedRows)
	}
	var storedRows int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+s.tables.rollupValues).Scan(&storedRows); err != nil {
		return fmt.Errorf("metric: count converted upstream 1.3.1 rollups: %w", err)
	}
	if storedRows != targetRows {
		return fmt.Errorf("metric: converted upstream 1.3.1 row count mismatch: expected=%d stored=%d", targetRows, storedRows)
	}
	s.reportMigrationProgress(MigrationPhaseNormalizingRollups, sourceRows, sourceRows, sourceRows)

	for _, name := range []string{source.rollups, source.series, source.labels, source.resolutions, source.definitions} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+name); err != nil {
			return fmt.Errorf("metric: remove converted upstream 1.3.1 table %s: %w", name, err)
		}
	}
	if err := s.createSQLiteV3CompatibilityObjects(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metric: commit upstream 1.3.1 SQLite migration: %w", err)
	}
	return nil
}

func (s *Store) copyUpstream131DefinitionsTx(ctx context.Context, tx *sql.Tx, source string) error {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		`SELECT name, type, unit, description, retention_days, metadata,
		        created_at_milli, updated_at_milli FROM %s ORDER BY name`, source,
	))
	if err != nil {
		return fmt.Errorf("metric: read upstream 1.3.1 definitions: %w", err)
	}
	type definitionRow struct {
		name, typ, unit, description, metadata string
		retentionDays                          int
		createdAtMilli, updatedAtMilli         int64
	}
	definitions := make([]definitionRow, 0)
	for rows.Next() {
		var item definitionRow
		var rawMetadata any
		if err := rows.Scan(&item.name, &item.typ, &item.unit, &item.description, &item.retentionDays, &rawMetadata, &item.createdAtMilli, &item.updatedAtMilli); err != nil {
			_ = rows.Close()
			return fmt.Errorf("metric: scan upstream 1.3.1 definition: %w", err)
		}
		metadata, err := decodeMap(rawMetadata)
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("metric: decode upstream 1.3.1 definition %q metadata: %w", item.name, err)
		}
		item.metadata, err = encodeMap(metadata)
		if err != nil {
			_ = rows.Close()
			return err
		}
		definitions = append(definitions, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (name, type, unit, description, retention_days, metadata, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, s.tables.definitions,
	))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range definitions {
		createdAt, err := upstream131MillisToNanos(item.createdAtMilli)
		if err != nil {
			return fmt.Errorf("metric: definition %q created time: %w", item.name, err)
		}
		updatedAt, err := upstream131MillisToNanos(item.updatedAtMilli)
		if err != nil {
			return fmt.Errorf("metric: definition %q updated time: %w", item.name, err)
		}
		if _, err := stmt.ExecContext(ctx, item.name, item.typ, item.unit, item.description, item.retentionDays, item.metadata, createdAt, updatedAt); err != nil {
			return fmt.Errorf("metric: write upstream 1.3.1 definition %q: %w", item.name, err)
		}
	}
	return nil
}

type upstream131RollupKey struct {
	seriesID       int64
	resolutionNano int64
	bucketNano     int64
}

type upstream131RollupRow struct {
	key               upstream131RollupKey
	labelID           int64
	count             int64
	sum, sumSq        float64
	min, max          float64
	firstVal, lastVal float64
	firstTS, lastTS   int64
	digest            []byte
	createdAt         int64
}

func (s *Store) copyUpstream131RollupsTx(ctx context.Context, tx *sql.Tx, source upstream131SourceTables, total int64) (int64, int64, error) {
	insert, err := tx.PrepareContext(ctx, fmt.Sprintf(
		`INSERT INTO %s
		 (series_id, resolution_nano, bucket_nano, count, sum, sum_sq, min_val, max_val,
		  first_val, first_ts, last_val, last_ts, digest, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.tables.rollupValues,
	))
	if err != nil {
		return 0, 0, fmt.Errorf("metric: prepare upstream 1.3.1 rollup copy: %w", err)
	}
	defer insert.Close()

	var cursor upstream131RollupKey
	var cursorLabel int64 = -1
	firstPage := true
	var currentKey upstream131RollupKey
	var current *rollupBucket
	var currentCreatedAt int64
	var copied, written int64

	flush := func() error {
		if current == nil {
			return nil
		}
		if _, err := insert.ExecContext(ctx,
			currentKey.seriesID, currentKey.resolutionNano, currentKey.bucketNano,
			current.count, current.sum, current.sumSq, current.min, current.max,
			current.firstVal, current.firstTS, current.lastVal, current.lastTS,
			current.digest.Encode(), currentCreatedAt,
		); err != nil {
			return fmt.Errorf("metric: write upstream 1.3.1 rollup series=%d resolution=%d bucket=%d: %w",
				currentKey.seriesID, currentKey.resolutionNano, currentKey.bucketNano, err)
		}
		written++
		return nil
	}

	for {
		where := ""
		args := []any{}
		if !firstPage {
			where = `WHERE (r.series_id, d.resolution_milli, r.bucket_milli, r.label_id) > (?, ?, ?, ?)`
			args = append(args, cursor.seriesID, cursor.resolutionNano/int64(1_000_000), cursor.bucketNano/int64(1_000_000), cursorLabel)
		}
		args = append(args, upstream131CopyBatchSize)
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT r.series_id, d.resolution_milli, r.bucket_milli, r.label_id,
			        r.count, r.sum, r.sum_sq, r.min_val, r.max_val,
			        r.first_val, r.first_ts_milli, r.last_val, r.last_ts_milli,
			        r.digest, r.created_at_milli, l.labels
			 FROM %s r
			 JOIN %s d ON d.id = r.resolution_id
			 JOIN %s l ON l.id = r.label_id
			 %s
			 ORDER BY r.series_id, d.resolution_milli, r.bucket_milli, r.label_id
			 LIMIT ?`, source.rollups, source.resolutions, source.labels, where,
		), args...)
		if err != nil {
			return copied, written, fmt.Errorf("metric: read upstream 1.3.1 rollups: %w", err)
		}
		page := make([]upstream131RollupRow, 0, upstream131CopyBatchSize)
		for rows.Next() {
			var item upstream131RollupRow
			var resolutionMilli, bucketMilli, firstMilli, lastMilli, createdMilli int64
			var rawLabels any
			if err := rows.Scan(
				&item.key.seriesID, &resolutionMilli, &bucketMilli, &item.labelID,
				&item.count, &item.sum, &item.sumSq, &item.min, &item.max,
				&item.firstVal, &firstMilli, &item.lastVal, &lastMilli,
				&item.digest, &createdMilli, &rawLabels,
			); err != nil {
				_ = rows.Close()
				return copied, written, fmt.Errorf("metric: scan upstream 1.3.1 rollup: %w", err)
			}
			if _, err := decodeMap(rawLabels); err != nil {
				_ = rows.Close()
				return copied, written, fmt.Errorf("metric: decode upstream 1.3.1 rollup labels: %w", err)
			}
			if item.count <= 0 {
				_ = rows.Close()
				return copied, written, fmt.Errorf("metric: invalid upstream 1.3.1 rollup count %d", item.count)
			}
			if resolutionMilli <= 0 {
				_ = rows.Close()
				return copied, written, fmt.Errorf("metric: invalid upstream 1.3.1 rollup resolution %dms", resolutionMilli)
			}
			if item.key.resolutionNano, err = upstream131MillisToNanos(resolutionMilli); err != nil {
				_ = rows.Close()
				return copied, written, err
			}
			if item.key.bucketNano, err = upstream131MillisToNanos(bucketMilli); err != nil {
				_ = rows.Close()
				return copied, written, err
			}
			if item.firstTS, err = upstream131MillisToNanos(firstMilli); err != nil {
				_ = rows.Close()
				return copied, written, err
			}
			if item.lastTS, err = upstream131MillisToNanos(lastMilli); err != nil {
				_ = rows.Close()
				return copied, written, err
			}
			if item.createdAt, err = upstream131MillisToNanos(createdMilli); err != nil {
				_ = rows.Close()
				return copied, written, err
			}
			if item.firstTS > item.lastTS {
				_ = rows.Close()
				return copied, written, fmt.Errorf("metric: invalid upstream 1.3.1 rollup timestamps: first=%d last=%d", item.firstTS, item.lastTS)
			}
			page = append(page, item)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return copied, written, err
		}
		if err := rows.Close(); err != nil {
			return copied, written, err
		}
		if len(page) == 0 {
			break
		}

		for _, item := range page {
			digest, err := decodeUpstream131Digest(item)
			if err != nil {
				return copied, written, fmt.Errorf("metric: decode upstream 1.3.1 digest series=%d bucket=%d: %w", item.key.seriesID, item.key.bucketNano, err)
			}
			sourceBucket := &rollupBucket{
				count: item.count, sum: item.sum, sumSq: item.sumSq, min: item.min, max: item.max,
				firstVal: item.firstVal, firstTS: item.firstTS,
				lastVal: item.lastVal, lastTS: item.lastTS, digest: digest,
			}
			if current == nil || currentKey != item.key {
				if err := flush(); err != nil {
					return copied, written, err
				}
				currentKey = item.key
				current = sourceBucket
				currentCreatedAt = item.createdAt
			} else {
				current.mergeStored(sourceBucket)
				if item.createdAt > currentCreatedAt {
					currentCreatedAt = item.createdAt
				}
			}
			copied++
			cursor = item.key
			cursorLabel = item.labelID
		}
		s.reportMigrationProgress(MigrationPhaseNormalizingRollups, copied, total, copied)
		firstPage = false
		if len(page) < upstream131CopyBatchSize {
			break
		}
	}
	if err := flush(); err != nil {
		return copied, written, err
	}
	return copied, written, nil
}

func upstream131MillisToNanos(value int64) (int64, error) {
	const scale = int64(1_000_000)
	if value > math.MaxInt64/scale || value < math.MinInt64/scale {
		return 0, fmt.Errorf("metric: upstream 1.3.1 millisecond timestamp %d overflows nanoseconds", value)
	}
	return value * scale, nil
}

func decodeUpstream131Digest(item upstream131RollupRow) (*TDigest, error) {
	if len(item.digest) > 0 {
		return DecodeTDigest(item.digest)
	}
	digest := NewTDigest(defaultTDigestCompression)
	if item.min == item.max {
		digest.Add(item.min, float64(item.count))
		return digest, nil
	}
	return nil, fmt.Errorf("missing t-digest for non-constant rollup with %d samples", item.count)
}
