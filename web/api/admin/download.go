package admin

import (
	"archive/zip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/web/api"
	_ "github.com/mattn/go-sqlite3"
)

type backupScope string

const (
	backupScopeFull   backupScope = "full"
	backupScopeConfig backupScope = "config"
)

var backupPersistentPaths = []string{
	"favicon.ico",
	"font.ttf",
	"secret.key",
	"theme",
}

var configOnlyEmptyTables = []string{
	"clipboards",
	"sessions",
	"task_results",
	"tasks",
	"logs",
	"traffic_daily_ledgers",
	"traffic_calibration_adjustments",
	"return_route_events",
	"return_route_statuses",
}

type configOnlyRuntimeReset struct {
	table       string
	assignments map[string]string
}

var configOnlyRuntimeResets = []configOnlyRuntimeReset{
	{table: "offline_notifications", assignments: map[string]string{"last_notified": "NULL"}},
	{table: "load_notifications", assignments: map[string]string{"last_notified": "NULL"}},
	{table: "ping_loss_notifications", assignments: map[string]string{
		"last_notified": "NULL",
		"alert_active":  "0",
	}},
}

var legacyMonitoringTables = map[string]struct{}{
	"records":           {},
	"records_long_term": {},
	"gpu_records":       {},
	"ping_records":      {},
}

func parseBackupScope(raw string) (backupScope, error) {
	switch backupScope(strings.ToLower(strings.TrimSpace(raw))) {
	case "", backupScopeFull:
		return backupScopeFull, nil
	case backupScopeConfig:
		return backupScopeConfig, nil
	default:
		return "", fmt.Errorf("unsupported backup scope %q", raw)
	}
}

func copyFile(srcPath, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()
	dest, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	defer dest.Close()
	if _, err := io.Copy(dest, src); err != nil {
		return fmt.Errorf("copy file: %w", err)
	}
	return nil
}

func copyDir(srcDir, destDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destDir, 0o755)
		}
		destination := filepath.Join(destDir, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		return copyFile(path, destination)
	})
}

func copyPersistentFiles(contentDir string) error {
	for _, relative := range backupPersistentPaths {
		source := filepath.Join(".", "data", relative)
		info, err := os.Stat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect persistent file %s: %w", relative, err)
		}
		destination := filepath.Join(contentDir, relative)
		if info.IsDir() {
			if err := copyDir(source, destination); err != nil {
				return fmt.Errorf("copy persistent directory %s: %w", relative, err)
			}
			continue
		}
		if err := copyFile(source, destination); err != nil {
			return fmt.Errorf("copy persistent file %s: %w", relative, err)
		}
	}
	return nil
}

func backupMainSQLite(destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create main database backup directory: %w", err)
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous main database backup: %w", err)
	}
	db := dbcore.GetDBInstance()
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get main database connection: %w", err)
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve main database backup path: %w", err)
	}
	safePath := strings.ReplaceAll(filepath.ToSlash(absolute), "'", "''")
	if _, err := sqlDB.Exec(fmt.Sprintf("VACUUM INTO '%s'", safePath)); err != nil {
		return fmt.Errorf("back up main SQLite database: %w", err)
	}
	return nil
}

func openSnapshotDatabase(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_foreign_keys=off", filepath.ToSlash(absolute))
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func snapshotObjectNames(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, type FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := make(map[string]string)
	for rows.Next() {
		var name, objectType string
		if err := rows.Scan(&name, &objectType); err != nil {
			return nil, err
		}
		objects[name] = objectType
	}
	return objects, rows.Err()
}

func snapshotColumnNames(ctx context.Context, db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = struct{}{}
	}
	return columns, rows.Err()
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func normalizePortableMetricConfig(ctx context.Context, db *sql.DB, objects map[string]string) error {
	if objects["configs"] != "table" {
		return nil
	}
	values := map[string]string{
		"metric_db_driver":        `"sqlite"`,
		"metric_db_dsn":           `"./data/metrics.db"`,
		"metric_migration_target": `"sqlite|./data/metrics.db"`,
	}
	for key, value := range values {
		if _, err := db.ExecContext(ctx, `INSERT INTO configs (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("normalize portable metric config %s: %w", key, err)
		}
	}
	return nil
}

func sanitizeConfigSnapshot(ctx context.Context, path string, scope backupScope) error {
	db, err := openSnapshotDatabase(path)
	if err != nil {
		return fmt.Errorf("open main database snapshot: %w", err)
	}
	defer db.Close()

	objects, err := snapshotObjectNames(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect main database snapshot: %w", err)
	}
	if err := normalizePortableMetricConfig(ctx, db, objects); err != nil {
		return err
	}
	if scope == backupScopeConfig {
		for _, table := range configOnlyEmptyTables {
			if objects[table] != "table" {
				continue
			}
			if _, err := db.ExecContext(ctx, "DELETE FROM "+quoteSQLiteIdentifier(table)); err != nil {
				return fmt.Errorf("remove transient data from %s: %w", table, err)
			}
		}
		for _, reset := range configOnlyRuntimeResets {
			if objects[reset.table] != "table" {
				continue
			}
			columns, err := snapshotColumnNames(ctx, db, reset.table)
			if err != nil {
				return fmt.Errorf("inspect runtime columns in %s: %w", reset.table, err)
			}
			assignments := make([]string, 0, len(reset.assignments))
			for column, value := range reset.assignments {
				if _, ok := columns[column]; !ok {
					continue
				}
				assignments = append(assignments, quoteSQLiteIdentifier(column)+" = "+value)
			}
			if len(assignments) == 0 {
				continue
			}
			if _, err := db.ExecContext(ctx, "UPDATE "+quoteSQLiteIdentifier(reset.table)+" SET "+strings.Join(assignments, ", ")); err != nil {
				return fmt.Errorf("reset runtime data in %s: %w", reset.table, err)
			}
		}
		for name, objectType := range objects {
			_, legacy := legacyMonitoringTables[name]
			if !legacy && !strings.HasPrefix(name, "metric_") {
				continue
			}
			if objectType != "table" && objectType != "view" {
				continue
			}
			keyword := "TABLE"
			if objectType == "view" {
				keyword = "VIEW"
			}
			if _, err := db.ExecContext(ctx, "DROP "+keyword+" IF EXISTS "+quoteSQLiteIdentifier(name)); err != nil {
				return fmt.Errorf("remove monitoring object %s: %w", name, err)
			}
		}
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("compact main database snapshot: %w", err)
	}
	return nil
}

func writeDirectoryToZip(writer *zip.Writer, contentDir string) error {
	return filepath.Walk(contentDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(contentDir, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		name := filepath.ToSlash(relative)
		if info.IsDir() {
			_, err := writer.CreateHeader(&zip.FileHeader{Name: name + "/", Method: zip.Deflate, Modified: info.ModTime()})
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: info.ModTime()})
		if err != nil {
			file.Close()
			return err
		}
		_, copyErr := io.Copy(entry, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func writeBackupMarkup(writer *zip.Writer, scope backupScope, now time.Time) error {
	content := "此文件为 Komari 备份标记文件，请勿删除。\nThis is a Komari backup markup file, please do not delete.\n\n" +
		"备份类型 / Backup Type: " + string(scope) + "\n" +
		"备份时间 / Backup Time: " + now.UTC().Format(time.RFC3339Nano)
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: "komari-backup-markup", Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	_, err = entry.Write([]byte(content))
	return err
}

func buildBackupArchive(path, contentDir string, scope backupScope, now time.Time) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	if err := writeDirectoryToZip(writer, contentDir); err != nil {
		writer.Close()
		file.Close()
		return err
	}
	if err := writeBackupMarkup(writer, scope, now); err != nil {
		writer.Close()
		file.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// DownloadBackup creates either a complete portable backup (including all
// monitoring history) or a configuration-only package compatible with the
// current fork and the latest upstream restore format.
func DownloadBackup(c *gin.Context) {
	scope, err := parseBackupScope(c.Query("scope"))
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if !flags.IsSQLite() {
		api.RespondError(c, http.StatusUnprocessableEntity, "当前主数据库不是 SQLite，无法生成可直接恢复的 Komari 备份")
		return
	}

	tempDir, err := os.MkdirTemp("", "komari-backup-*")
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("创建备份临时目录失败: %v", err))
		return
	}
	defer os.RemoveAll(tempDir)
	contentDir := filepath.Join(tempDir, "content")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("创建备份内容目录失败: %v", err))
		return
	}
	if err := copyPersistentFiles(contentDir); err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("复制持久化配置失败: %v", err))
		return
	}

	mainSnapshot := filepath.Join(contentDir, "komari.db")
	if err := backupMainSQLite(mainSnapshot); err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if err := sanitizeConfigSnapshot(c.Request.Context(), mainSnapshot, scope); err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if scope == backupScopeFull {
		if err := metricstore.BackupSQLite(c.Request.Context(), filepath.Join(contentDir, "metrics.db")); err != nil {
			api.RespondError(c, http.StatusUnprocessableEntity, "完整备份未生成："+err.Error()+"。若使用 MySQL/PostgreSQL，请同时使用数据库自身的备份工具；也可以改用仅配置导出。")
			return
		}
	}

	now := time.Now().UTC()
	archivePath := filepath.Join(tempDir, "backup.zip")
	if err := buildBackupArchive(archivePath, contentDir, scope, now); err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("生成备份压缩包失败: %v", err))
		return
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, fmt.Sprintf("读取备份压缩包失败: %v", err))
		return
	}
	defer archive.Close()
	filename := fmt.Sprintf("Komari-%s-%s.zip", scope, now.Format("20060102-150405"))
	c.Writer.Header().Set("Content-Type", "application/zip")
	c.Writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	http.ServeContent(c.Writer, c.Request, filename, now, archive)
}
