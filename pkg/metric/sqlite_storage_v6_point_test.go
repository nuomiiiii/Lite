package metric

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"
)

func sqliteV6TestPoints(base int64, count int, offset float64) []sqliteV4BlockPoint {
	points := make([]sqliteV4BlockPoint, count)
	for i := range points {
		points[i] = sqliteV4BlockPoint{
			timestamp: base + int64(i)*3*time.Second.Nanoseconds() + int64(i%7),
			valueBits: math.Float64bits(offset + math.Sin(float64(i)/13)),
			labels:    []string{`{}`, `{"source":"agent"}`}[i%2],
			createdAt: base + int64(i/20)*time.Millisecond.Nanoseconds(),
		}
	}
	return points
}

func TestSQLiteV6SharedPointCodecPreservesEveryBit(t *testing.T) {
	points := sqliteV6TestPoints(time.Date(2026, 7, 30, 8, 0, 0, 123, time.UTC).UnixNano(), 4096, 1000)
	points[0].valueBits = math.Float64bits(math.Copysign(0, -1))
	points[1].valueBits = math.Float64bits(math.SmallestNonzeroFloat64)
	points[2].valueBits = 0x7ff8000000001234

	encoded, err := encodeSQLiteV6SharedPointBlock(points)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSQLiteStoredPointBlock(
		encoded.codec, encoded.count, encoded.checksum, encoded.payload,
		encoded.axisCodec, encoded.axisChecksum, encoded.axisPayload,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sqliteV4PointsEqual(points, decoded) {
		t.Fatal("SQLite V6 shared point codec changed a timestamp, float bit pattern, label, or creation time")
	}
}

func TestSQLiteV6PointAxesShareOnlyIdenticalTimelines(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)
	base := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC).UnixNano()
	first := sqliteV6TestPoints(base, 32, 10)
	second := sqliteV6TestPoints(base, 32, 20)
	different := sqliteV6TestPoints(base+int64(time.Nanosecond), 32, 30)
	for _, name := range []string{"axis.first", "axis.second", "axis.different"} {
		if err := store.CreateMetric(ctx, Definition{Name: name, Type: TypeGauge, RetentionDays: 7}); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, name := range []string{"axis.first", "axis.second", "axis.different"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metric_series (metric_name, entity_id, tags_hash, tags) VALUES (?, 'node', '', '{}')`, name); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, metric_name FROM metric_series ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64)
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for name, points := range map[string][]sqliteV4BlockPoint{
		"axis.first": first, "axis.second": second, "axis.different": different,
	} {
		if err := store.writeSQLiteV4BlocksTx(ctx, tx, ids[name], points); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var axisRows, blockRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_point_axes`).Scan(&axisRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM metric_point_blocks`).Scan(&blockRows); err != nil {
		t.Fatal(err)
	}
	if axisRows != 2 || blockRows != 3 {
		t.Fatalf("shared point axes=%d blocks=%d, want axes=2 blocks=3", axisRows, blockRows)
	}
	var firstAxis, secondAxis, differentAxis int64
	queryAxis := func(name string, target *int64) {
		t.Helper()
		if err := store.db.QueryRowContext(ctx, `SELECT b.axis_id FROM metric_point_blocks b JOIN metric_series s ON s.id = b.series_id WHERE s.metric_name = ?`, name).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	queryAxis("axis.first", &firstAxis)
	queryAxis("axis.second", &secondAxis)
	queryAxis("axis.different", &differentAxis)
	if firstAxis != secondAxis || firstAxis == differentAxis {
		t.Fatalf("unexpected point-axis identities: first=%d second=%d different=%d", firstAxis, secondAxis, differentAxis)
	}
}

func TestSQLiteV6MigratesLegacyPointBlocksLosslessly(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)
	points := sqliteV6TestPoints(time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC).UnixNano(), 80, 42)
	legacy, err := encodeSQLiteV4Block(points)
	if err != nil {
		t.Fatal(err)
	}
	seriesID, err := insertSQLiteV6LegacyPointBlock(ctx, store, "legacy.point", legacy)
	if err != nil {
		t.Fatal(err)
	}

	blocks, migratedPoints, err := store.migrateSQLiteV6SharedPointBlocks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if blocks != 1 || migratedPoints != int64(len(points)) {
		t.Fatalf("migrated blocks=%d points=%d", blocks, migratedPoints)
	}
	var codec int
	var axisID sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `SELECT codec, axis_id FROM metric_point_blocks`).Scan(&codec, &axisID); err != nil {
		t.Fatal(err)
	}
	if codec != sqliteV6SharedPointBlockCodec || !axisID.Valid {
		t.Fatalf("legacy block was not converted: codec=%d axis=%v", codec, axisID)
	}
	got, err := store.loadAllSQLiteV4BlockPoints(ctx, store.db, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	if !sqliteV4PointsEqual(points, got) {
		t.Fatal("legacy point migration changed data")
	}
}

func TestSQLiteV6PointMigrationFailureKeepsLegacyBlock(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)
	points := sqliteV6TestPoints(time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC).UnixNano(), 8, 42)
	legacy, err := encodeSQLiteV4Block(points)
	if err != nil {
		t.Fatal(err)
	}
	legacy.payload[len(legacy.payload)-1] ^= 0x80
	if _, err := insertSQLiteV6LegacyPointBlock(ctx, store, "broken.point", legacy); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := store.db.QueryRowContext(ctx, `SELECT payload FROM metric_point_blocks`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.migrateSQLiteV6SharedPointBlocks(ctx); err == nil {
		t.Fatal("corrupt legacy point block unexpectedly migrated")
	}
	var codec int
	var axisID sql.NullInt64
	var after []byte
	if err := store.db.QueryRowContext(ctx, `SELECT codec, axis_id, payload FROM metric_point_blocks`).Scan(&codec, &axisID, &after); err != nil {
		t.Fatal(err)
	}
	if codec != sqliteV4BlockCodec || axisID.Valid || !bytesEqual(before, after) {
		t.Fatalf("failed migration changed legacy block: codec=%d axis=%v", codec, axisID)
	}
}

func insertSQLiteV6LegacyPointBlock(ctx context.Context, store *Store, name string, encoded sqliteV4EncodedBlock) (int64, error) {
	if err := store.CreateMetric(ctx, Definition{Name: name, Type: TypeGauge, RetentionDays: 7}); err != nil {
		return 0, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO metric_series (metric_name, entity_id, tags_hash, tags) VALUES (?, 'node', '', '{}')`, name)
	if err != nil {
		return 0, err
	}
	seriesID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO metric_point_blocks (series_id, start_nano, end_nano, point_count, codec, checksum, payload, axis_id) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		seriesID, encoded.startNano, encoded.endNano, encoded.count, encoded.codec, int64(encoded.checksum), encoded.payload); err != nil {
		return 0, err
	}
	return seriesID, tx.Commit()
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
