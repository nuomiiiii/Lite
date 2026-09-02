package dbcore

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/billing"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/migrations"
	logger "github.com/nuomiiiii/lite/utils/log"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// zipDirectoryExcluding 将 srcDir 打包为 dstZip，exclude 是绝对路径集合需要排除
func zipDirectoryExcluding(srcDir, dstZip string, exclude map[string]struct{}) error {
	// 规范化排除路径为绝对路径
	normExclude := make(map[string]struct{}, len(exclude))
	for p := range exclude {
		abs, _ := filepath.Abs(p)
		normExclude[abs] = struct{}{}
	}

	out, err := os.Create(dstZip)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	absSrc, _ := filepath.Abs(srcDir)
	walkErr := filepath.Walk(absSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// 排除 backup.zip 本身
		if _, ok := normExclude[path]; ok {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// 计算 zip 内相对路径
		rel, err := filepath.Rel(absSrc, path)
		if err != nil {
			return err
		}
		// 根目录跳过
		if rel == "." {
			return nil
		}
		// 替换为正斜杠
		zipName := filepath.ToSlash(rel)

		if info.IsDir() {
			_, err := zw.Create(zipName + "/")
			return err
		}
		// 普通文件
		fh, err := os.Open(path)
		if err != nil {
			return err
		}
		w, err := zw.Create(zipName)
		if err != nil {
			fh.Close()
			return err
		}
		if _, err := io.Copy(w, fh); err != nil {
			fh.Close()
			return err
		}
		fh.Close()
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	return zw.Close()
}

// removeAllInDirExcept 删除 dir 下除 exclude 指定绝对路径外的所有文件和文件夹
func removeAllInDirExcept(dir string, exclude map[string]struct{}) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	normExclude := make(map[string]struct{}, len(exclude))
	for p := range exclude {
		abs, _ := filepath.Abs(p)
		normExclude[abs] = struct{}{}
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(absDir, e.Name())
		if _, ok := normExclude[full]; ok {
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			return err
		}
	}
	return nil
}

// unzipToDir 将 zipPath 解压到 dstDir，包含路径遍历保护
func unzipToDir(zipPath, dstDir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}
	absDst, _ := filepath.Abs(dstDir)

	for _, f := range zr.File {
		// 构造目标路径并做路径遍历保护
		cleanName := filepath.Clean(f.Name)
		targetPath := filepath.Join(absDst, cleanName)
		if !strings.HasPrefix(targetPath, absDst+string(os.PathSeparator)) && targetPath != absDst {
			return fmt.Errorf("illegal file path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(targetPath)
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

func validateRestoredSQLite(path, label string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open restored %s: %w", label, err)
	}
	defer file.Close()
	header := make([]byte, 16)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read restored %s header: %w", label, err)
	}
	if string(header) != "SQLite format 3\x00" {
		return fmt.Errorf("restored %s is not a valid SQLite database", label)
	}
	return nil
}

// restoreStagedBackup extracts and validates the entire archive before it
// replaces data/. Directory renames keep the previous data intact if the new
// package cannot be prepared or published.
type stagedRestore struct {
	dataDir         string
	previousDir     string
	stageDir        string
	preRestorePath  string
	pendingMarker   string
	committedMarker string
	finished        bool
	mu              sync.Mutex
}

const (
	restorePendingMarkerName         = ".lite-restore-pending.json"
	restoreCommittedMarkerName       = ".lite-restore-committed"
	legacyRestorePendingMarkerName   = ".komari-restore-pending.json"
	legacyRestoreCommittedMarkerName = ".komari-restore-committed"
	restoreStagePrefix               = ".lite-restore-"
	restorePreviousPrefix            = ".lite-restore-old-"
	legacyRestoreStagePrefix         = ".komari-restore-"
	legacyRestorePreviousPrefix      = ".komari-restore-old-"
)

type restoreJournal struct {
	DataDir     string `json:"data_dir"`
	PreviousDir string `json:"previous_dir"`
	StageDir    string `json:"stage_dir"`
}

func restoreMarkerPaths(dataDir string) (string, string, string, error) {
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve restore data directory: %w", err)
	}
	parent := filepath.Dir(absDataDir)
	return absDataDir,
		filepath.Join(parent, restorePendingMarkerName),
		filepath.Join(parent, restoreCommittedMarkerName),
		nil
}

func writeRestoreMarker(path string, content []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".lite-restore-marker-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func resolveRestoreJournalPath(parent, name string, prefixes ...string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid restore journal path %q", name)
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return filepath.Join(parent, name), nil
		}
	}
	return "", fmt.Errorf("invalid restore journal path %q", name)
}

func readRestoreJournal(path, dataDir string) (restoreJournal, string, string, error) {
	var journal restoreJournal
	content, err := os.ReadFile(path)
	if err != nil {
		return journal, "", "", err
	}
	if err := json.Unmarshal(content, &journal); err != nil {
		return journal, "", "", fmt.Errorf("decode interrupted restore journal: %w", err)
	}
	if journal.DataDir != filepath.Base(dataDir) {
		return journal, "", "", fmt.Errorf("restore journal data directory %q does not match %q", journal.DataDir, filepath.Base(dataDir))
	}
	parent := filepath.Dir(dataDir)
	previousDir, err := resolveRestoreJournalPath(parent, journal.PreviousDir, restorePreviousPrefix, legacyRestorePreviousPrefix)
	if err != nil {
		return journal, "", "", err
	}
	stageDir, err := resolveRestoreJournalPath(parent, journal.StageDir, restoreStagePrefix, legacyRestoreStagePrefix)
	if err != nil || strings.HasPrefix(journal.StageDir, restorePreviousPrefix) || strings.HasPrefix(journal.StageDir, legacyRestorePreviousPrefix) {
		if err == nil {
			err = fmt.Errorf("invalid restore stage path %q", journal.StageDir)
		}
		return journal, "", "", err
	}
	return journal, previousDir, stageDir, nil
}

func removeRestoreMarkers(pendingMarker, committedMarker string) {
	for _, marker := range []string{pendingMarker, committedMarker} {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			logger.Errorf("dbcore", "[restore] failed to remove transaction marker %s: %v", marker, err)
		}
	}
}

func retireInterruptedRestoreArchive(dataDir string) {
	archivePath := filepath.Join(dataDir, "backup.zip")
	failedPath := filepath.Join(dataDir, "backup.interrupted-"+time.Now().UTC().Format("20060102-150405.000000000")+".zip")
	if err := os.Rename(archivePath, failedPath); err != nil && !os.IsNotExist(err) {
		logger.Errorf("dbcore", "[restore] previous data restored but interrupted package could not be renamed: %v", err)
	}
}

func recoverInterruptedRestore(dataDir string) error {
	absDataDir, pendingMarker, committedMarker, err := restoreMarkerPaths(dataDir)
	if err != nil {
		return err
	}
	legacyPending := filepath.Join(filepath.Dir(absDataDir), legacyRestorePendingMarkerName)
	legacyCommitted := filepath.Join(filepath.Dir(absDataDir), legacyRestoreCommittedMarkerName)
	if _, err := os.Stat(pendingMarker); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect interrupted restore marker: %w", err)
		}
		if _, err := os.Stat(legacyPending); err == nil {
			pendingMarker = legacyPending
			committedMarker = legacyCommitted
		} else if os.IsNotExist(err) {
			for _, marker := range []string{committedMarker, legacyCommitted} {
				if removeErr := os.Remove(marker); removeErr != nil && !os.IsNotExist(removeErr) {
					return fmt.Errorf("remove completed restore marker: %w", removeErr)
				}
			}
			return nil
		} else {
			return fmt.Errorf("inspect interrupted restore marker: %w", err)
		}
	}

	_, previousDir, stageDir, err := readRestoreJournal(pendingMarker, absDataDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(committedMarker); err == nil {
		if err := os.RemoveAll(previousDir); err != nil {
			return fmt.Errorf("finish committed restore cleanup: %w", err)
		}
		if err := os.RemoveAll(stageDir); err != nil {
			return fmt.Errorf("remove committed restore staging directory: %w", err)
		}
		removeRestoreMarkers(pendingMarker, committedMarker)
		logger.Infof("dbcore", "[restore] completed cleanup for a restore committed before interruption")
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect committed restore marker: %w", err)
	}

	if _, err := os.Stat(previousDir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect previous restore directory: %w", err)
		}
		if _, err := os.Stat(absDataDir); err != nil {
			return fmt.Errorf("restore transaction is incomplete and neither active nor previous data is available: %w", err)
		}
		if err := os.RemoveAll(stageDir); err != nil {
			return fmt.Errorf("remove unpublished restore staging directory: %w", err)
		}
		removeRestoreMarkers(pendingMarker, committedMarker)
		return nil
	}

	failedDir, err := os.MkdirTemp(filepath.Dir(absDataDir), ".lite-restore-interrupted-*")
	if err != nil {
		return fmt.Errorf("reserve interrupted restore path: %w", err)
	}
	if err := os.RemoveAll(failedDir); err != nil {
		return fmt.Errorf("prepare interrupted restore path: %w", err)
	}
	activeMoved := false
	if _, err := os.Stat(absDataDir); err == nil {
		if err := relocateDirectory(absDataDir, failedDir); err != nil {
			return fmt.Errorf("move interrupted restored data aside: %w", err)
		}
		activeMoved = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect interrupted restored data: %w", err)
	}
	if err := relocateDirectory(previousDir, absDataDir); err != nil {
		if activeMoved {
			_ = relocateDirectory(failedDir, absDataDir)
		}
		return fmt.Errorf("recover previous data after interrupted restore: %w", err)
	}
	retireInterruptedRestoreArchive(absDataDir)
	if activeMoved {
		if err := os.RemoveAll(failedDir); err != nil {
			logger.Errorf("dbcore", "[restore] previous data recovered but interrupted data could not be removed: %v", err)
		}
	}
	if err := os.RemoveAll(stageDir); err != nil {
		logger.Errorf("dbcore", "[restore] previous data recovered but staging directory could not be removed: %v", err)
	}
	removeRestoreMarkers(pendingMarker, committedMarker)
	logger.Infof("dbcore", "[restore] interrupted startup detected; previous data recovered")
	return nil
}

func (r *stagedRestore) Commit() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return nil
	}
	if err := writeRestoreMarker(r.committedMarker, []byte("committed\n")); err != nil {
		return fmt.Errorf("persist committed restore state: %w", err)
	}
	r.finished = true
	if err := os.RemoveAll(r.previousDir); err != nil {
		logger.Errorf("dbcore", "[restore] backup was verified and committed, but the previous staging directory could not be removed: %v", err)
		return nil
	}
	removeRestoreMarkers(r.pendingMarker, r.committedMarker)
	logger.Infof("dbcore", "[restore] backup verified and committed; previous data saved to %s", r.preRestorePath)
	return nil
}

func (r *stagedRestore) Rollback() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return nil
	}
	failedDir, err := os.MkdirTemp(filepath.Dir(r.dataDir), ".lite-restore-failed-*")
	if err != nil {
		return fmt.Errorf("reserve failed restore path: %w", err)
	}
	if err := os.RemoveAll(failedDir); err != nil {
		return fmt.Errorf("prepare failed restore path: %w", err)
	}
	if err := relocateDirectory(r.dataDir, failedDir); err != nil {
		return fmt.Errorf("move failed restored data aside: %w", err)
	}
	if err := relocateDirectory(r.previousDir, r.dataDir); err != nil {
		_ = relocateDirectory(failedDir, r.dataDir)
		return fmt.Errorf("restore previous data directory: %w", err)
	}
	failedArchive := filepath.Join(r.dataDir, "backup.failed-"+time.Now().UTC().Format("20060102-150405")+".zip")
	if err := os.Rename(filepath.Join(r.dataDir, "backup.zip"), failedArchive); err != nil && !os.IsNotExist(err) {
		logger.Errorf("dbcore", "[restore] previous data restored but failed package could not be renamed: %v", err)
	}
	if err := os.RemoveAll(failedDir); err != nil {
		logger.Errorf("dbcore", "[restore] previous data restored but failed staging directory could not be removed: %v", err)
	}
	removeRestoreMarkers(r.pendingMarker, r.committedMarker)
	r.finished = true
	logger.Infof("dbcore", "[restore] startup validation failed; previous data restored")
	return nil
}

func restoreStagedBackup(dataDir string) (*stagedRestore, error) {
	if err := recoverInterruptedRestore(dataDir); err != nil {
		return nil, fmt.Errorf("recover interrupted restore: %w", err)
	}
	backupZipPath := filepath.Join(dataDir, "backup.zip")
	if _, err := os.Stat(backupZipPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect staged backup: %w", err)
	}

	stageDir, err := os.MkdirTemp(filepath.Dir(dataDir), ".lite-restore-*")
	if err != nil {
		return nil, fmt.Errorf("create restore staging directory: %w", err)
	}
	stagePublished := false
	defer func() {
		if !stagePublished {
			_ = os.RemoveAll(stageDir)
		}
	}()
	if err := unzipToDir(backupZipPath, stageDir); err != nil {
		return nil, fmt.Errorf("extract backup into staging directory: %w", err)
	}
	mainDB, err := alignStagedMainDatabase(stageDir)
	if err != nil {
		return nil, fmt.Errorf("adopt restored Komari database: %w", err)
	}
	if err := validateRestoredSQLite(mainDB, filepath.Base(mainDB)); err != nil {
		return nil, err
	}
	metricsPath := filepath.Join(stageDir, "metrics.db")
	if _, err := os.Stat(metricsPath); err == nil {
		if err := validateRestoredSQLite(metricsPath, "metrics.db"); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect restored metrics.db: %w", err)
	}
	_ = os.Remove(filepath.Join(stageDir, "lite-backup-markup"))
	_ = os.Remove(filepath.Join(stageDir, "komari-backup-markup"))
	if err := os.MkdirAll(filepath.Join(stageDir, "theme"), 0o755); err != nil {
		return nil, fmt.Errorf("prepare restored theme directory: %w", err)
	}

	backupDir := filepath.Join(filepath.Dir(dataDir), "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return nil, fmt.Errorf("create pre-restore backup directory: %w", err)
	}
	timestamp := time.Now().UTC().Format("20060102-150405")
	preRestorePath := filepath.Join(backupDir, fmt.Sprintf("pre-restore-%s.zip", timestamp))
	if err := zipDirectoryExcluding(dataDir, preRestorePath, map[string]struct{}{backupZipPath: {}}); err != nil {
		return nil, fmt.Errorf("preserve current data before restore: %w", err)
	}

	oldDir, err := os.MkdirTemp(filepath.Dir(dataDir), ".lite-restore-old-*")
	if err != nil {
		return nil, fmt.Errorf("reserve previous data path: %w", err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		return nil, fmt.Errorf("prepare previous data path: %w", err)
	}
	absDataDir, pendingMarker, committedMarker, err := restoreMarkerPaths(dataDir)
	if err != nil {
		return nil, err
	}
	journal := restoreJournal{
		DataDir:     filepath.Base(absDataDir),
		PreviousDir: filepath.Base(oldDir),
		StageDir:    filepath.Base(stageDir),
	}
	journalData, err := json.Marshal(journal)
	if err != nil {
		return nil, fmt.Errorf("encode restore transaction journal: %w", err)
	}
	if err := writeRestoreMarker(pendingMarker, journalData); err != nil {
		return nil, fmt.Errorf("persist restore transaction journal: %w", err)
	}
	if err := relocateDirectory(dataDir, oldDir); err != nil {
		removeRestoreMarkers(pendingMarker, committedMarker)
		return nil, fmt.Errorf("move current data aside: %w", err)
	}
	if err := relocateDirectory(stageDir, dataDir); err != nil {
		rollbackErr := relocateDirectory(oldDir, dataDir)
		if rollbackErr != nil {
			return nil, fmt.Errorf("publish restored data: %v; restore previous data: %w", err, rollbackErr)
		}
		removeRestoreMarkers(pendingMarker, committedMarker)
		return nil, fmt.Errorf("publish restored data: %w", err)
	}
	stagePublished = true
	logger.Infof("dbcore", "[restore] backup published for startup validation; previous data remains available")
	return &stagedRestore{
		dataDir:         dataDir,
		previousDir:     oldDir,
		stageDir:        stageDir,
		preRestorePath:  preRestorePath,
		pendingMarker:   pendingMarker,
		committedMarker: committedMarker,
	}, nil
}

var (
	instance       *gorm.DB
	once           sync.Once
	initErr        error
	pendingRestore *stagedRestore
)

// SystemVersionKey 是记录“上次启动所用版本标识”的配置键（存于 configs 表）。
// 取代旧的 ./data/.komari-version 文件：版本标识随配置库一起备份/恢复，
// 也避免额外的裸文件依赖。
const SystemVersionKey = "system_version"

// versionID 是当前构建的版本标识，由 SetVersionID 在 Initialize 前注入。
var versionID string

// dbFileExistedAtStartup 记录本次进程启动、打开数据库之前 lite.db 是否已存在，
// 用于区分“全新安装”与“从旧版升级（无版本标记）”。在 doInitialize 打开数据库
// 之前采集。
var dbFileExistedAtStartup bool

// preserveLegacyHTTPSListen keeps the previous HTTPS listen address when
// upgrading an existing Komari / Komari Lite data directory. Missing keys are
// filled with :35938 instead of Lite's new-install default :36888.
func preserveLegacyHTTPSListen() error {
	if instance == nil {
		return nil
	}
	if !KeepLegacyHTTPListen(filepath.Dir(resolveDatabaseFile())) {
		return nil
	}
	var count int64
	if err := instance.Model(&config.ConfigItem{}).Where("key = ?", config.HTTPSListenKey).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return config.Set(config.HTTPSListenKey, ":35938")
}

// SetVersionID 设置当前构建的版本标识（通常为 CurrentVersion+"-"+VersionHash），
// 用于版本升级检测与自动备份。应在 Initialize() 之前调用；为空则跳过升级备份。
func SetVersionID(id string) {
	versionID = id
}

// resolveDatabaseFile 返回当前使用的 SQLite 数据库文件路径。
func resolveDatabaseFile() string {
	dbFile := flags.DatabaseFile
	if dbFile == "" {
		dbFile = "./data/lite.db"
	}
	return dbFile
}

// backupOnVersionUpgrade 在检测到版本升级时，把当前 ./data 打包到
// ./backup/upgrade-{time}.zip，便于升级（含 metrics 迁移）异常时回滚。
//
// 版本标识存放于配置库（configs 表，键 system_version），因此本函数必须在
// config.SetDb 之后、一次性 metrics 迁移（InitStores）之前调用。
//
// 触发规则：
//   - versionID 为空：跳过（未注入版本，如部分测试场景）。
//   - 配置中无版本且启动前无数据库文件：全新安装，仅写版本，不备份。
//   - 配置中无版本但启动前已有数据库文件：从无版本标记的旧稳定版升级，备份。
//   - 配置中版本与当前不同：版本升级，备份。
//   - 配置中版本与当前一致：无需备份。
//
// 备份失败不阻止启动，但打印明确错误；备份成功（或无需备份）后写入/更新版本。
func backupOnVersionUpgrade() {
	if versionID == "" {
		return
	}

	prevVersion, readErr := config.GetAs[string](SystemVersionKey)
	prevVersion = strings.TrimSpace(prevVersion)
	versionRecorded := readErr == nil && prevVersion != ""

	// 版本未变化，无需备份。
	if versionRecorded && prevVersion == versionID {
		return
	}

	// 全新安装：配置中无版本且启动前无数据库文件，直接写版本不备份。
	if !versionRecorded && !dbFileExistedAtStartup {
		writeVersionMarker()
		return
	}

	// 需要备份（升级或从旧稳定版首次带版本标记启动）。
	// 先做一次 WAL checkpoint，确保 lite.db 主文件包含最新数据，
	// 避免备份出的库缺少仍留在 -wal 中的写入。
	if instance != nil {
		instance.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
	}

	if err := os.MkdirAll("./backup", 0755); err != nil {
		logger.Errorf("dbcore", "[upgrade-backup] failed to create backup dir: %v", err)
		return
	}
	tsName := time.Now().UTC().Format("20060102-150405")
	bakPath := filepath.Join("./backup", fmt.Sprintf("upgrade-%s.zip", tsName))
	backupZipPath := filepath.Join(".", "data", "backup.zip")
	if zipErr := zipDirectoryExcluding("./data", bakPath, map[string]struct{}{backupZipPath: {}}); zipErr != nil {
		logger.Errorf("dbcore", "[upgrade-backup] failed to backup ./data before upgrade (from %q to %q): %v", prevVersion, versionID, zipErr)
		return
	}
	logger.Infof("dbcore", "[upgrade-backup] ./data backed up to %s before upgrade (from %q to %q)", bakPath, prevVersion, versionID)

	writeVersionMarker()
}

// writeVersionMarker 将当前 versionID 写入配置库。
func writeVersionMarker() {
	if err := config.Set(SystemVersionKey, versionID); err != nil {
		logger.Errorf("dbcore", "[upgrade-backup] failed to persist version marker: %v", err)
	}
}

func buildSQLiteDSN(databaseFile string) string {
	if databaseFile == "" {
		databaseFile = "./data/lite.db"
	}

	params := "_busy_timeout=5000&_txlock=immediate&_journal_mode=WAL&_synchronous=NORMAL"
	separator := "?"
	if strings.Contains(databaseFile, "?") {
		separator = "&"
	}

	if strings.HasPrefix(databaseFile, "file:") {
		return databaseFile + separator + params
	}

	if databaseFile == ":memory:" {
		return "file::memory:?cache=shared&" + params
	}

	return "file:" + filepath.ToSlash(databaseFile) + separator + params
}

// Initialize 显式初始化数据库连接与表结构，仅执行一次。
// 与 GetDBInstance 不同，Initialize 返回错误而非直接退出进程，
// 便于启动生命周期统一处理错误、以及在测试/CLI 命令中做隔离。
func Initialize() error {
	once.Do(func() {
		initErr = doInitialize()
	})
	return initErr
}

// GetDBInstance 返回全局数据库实例。
// 为兼容既有大量调用点，这里保留“出错即退出”的语义；
// 需要错误处理的启动流程应优先调用 Initialize()。
func GetDBInstance() *gorm.DB {
	if err := Initialize(); err != nil {
		logger.Fatalf("dbcore", "Failed to initialize database: %v", err)
	}
	return instance
}

// Close 关闭底层数据库连接，供关闭流程调用。
func Close() error {
	var closeErr error
	if instance != nil {
		sqlDB, err := instance.DB()
		if err != nil {
			closeErr = err
		} else {
			closeErr = sqlDB.Close()
		}
	}
	rollbackErr := RollbackPendingRestore()
	return errors.Join(closeErr, rollbackErr)
}

func CommitPendingRestore() error {
	if pendingRestore == nil {
		return nil
	}
	if err := pendingRestore.Commit(); err != nil {
		return err
	}
	pendingRestore = nil
	return nil
}

func RollbackPendingRestore() error {
	if pendingRestore == nil {
		return nil
	}
	err := pendingRestore.Rollback()
	if err == nil {
		pendingRestore = nil
	}
	return err
}

func doInitialize() error {
	var err error

	// Publish the staged package, but keep the previous directory until every
	// startup migration, including metric storage, has completed successfully.
	pendingRestore, err = restoreStagedBackup(filepath.Join(".", "data"))
	if err != nil {
		return fmt.Errorf("restore staged backup: %w", err)
	}
	if err := adoptLegacyKomariSQLite(); err != nil {
		return fmt.Errorf("adopt Komari Lite database: %w", err)
	}
	initialized := false
	defer func() {
		if initialized || pendingRestore == nil {
			return
		}
		if instance != nil {
			if sqlDB, closeErr := instance.DB(); closeErr == nil {
				_ = sqlDB.Close()
			}
		}
		_ = RollbackPendingRestore()
	}()

	// 记录“打开数据库之前”lite.db 是否已存在，用于区分全新安装与旧版升级。
	// 必须在（可能的）恢复逻辑之后、gorm.Open 之前采集：恢复会解压出旧库，
	// gorm.Open 会创建空库。
	if _, statErr := os.Stat(resolveDatabaseFile()); statErr == nil {
		dbFileExistedAtStartup = true
	}

	logConfig := &gorm.Config{
		Logger:  logger.NewGormLogger(),
		NowFunc: func() time.Time { return time.Now().UTC() },
	}

	// 根据数据库类型选择不同的连接方式
	switch flags.ApplyDatabaseTypeNormalization() {
	case flags.DatabaseTypeSQLite:
		// SQLite 连接
		// 通过 DSN 传入 _busy_timeout / _txlock 等参数，确保连接池中的每一条连接
		// 都生效：
		//   - _busy_timeout=5000：遇到写锁时最多等待 5s 再返回，避免瞬时
		//     "database is locked" 直接失败（仅靠后续 PRAGMA Exec 只作用于
		//     当时执行该语句的单条连接，池内其它连接不生效）。
		//   - _txlock=immediate：事务一开始即获取写锁，避免「先 SELECT 后写」
		//     的锁升级在并发写入下产生死锁式的立即 SQLITE_BUSY。
		//   - _journal_mode=WAL / _synchronous=NORMAL：与下方 PRAGMA 保持一致，
		//     在 DSN 层为所有连接预设。
		dsn := buildSQLiteDSN(flags.DatabaseFile)
		instance, err = gorm.Open(sqlite.Open(dsn), logConfig)
		if err != nil {
			return fmt.Errorf("failed to connect to SQLite3 database: %w", err)
		}
		if sqlDB, dbErr := instance.DB(); dbErr == nil {
			// SQLite 同一时刻只允许一个写者；限制连接数可避免连接池层面的写写竞争。
			// 负载历史每分钟会执行包含读和写的事务，若连接池允许多个连接，容易与
			// ping 结果等短写入撞锁并导致整批负载记录回滚。
			sqlDB.SetMaxOpenConns(1)
			sqlDB.SetMaxIdleConns(1)
			sqlDB.SetConnMaxLifetime(0)
		} else {
			logger.Errorf("dbcore", "Failed to access underlying sql.DB for SQLite tuning: %v", dbErr)
		}
		instance.Exec("PRAGMA wal = ON;")
		if err := instance.Exec("PRAGMA journal_mode = WAL;").Error; err != nil {
			logger.Errorf("dbcore", "Failed to enable WAL mode for SQLite: %v", err)
		}
		instance.Exec("PRAGMA synchronous = NORMAL;")
		instance.Exec("PRAGMA busy_timeout = 5000;")
		instance.Exec("PRAGMA wal_autocheckpoint = 256;")
		instance.Exec("PRAGMA journal_size_limit = 1048576;")
		instance.Exec(fmt.Sprintf("PRAGMA cache_size = -%d;", mainDatabaseCacheSizeKB()))
		instance.Exec("PRAGMA temp_store = MEMORY;")
		instance.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
	default:
		return fmt.Errorf("unsupported database type: %s (supported: %s)", flags.DatabaseType, flags.SupportedDatabaseTypes())
	}
	if err := migrations.Run(migrations.Context{DB: instance}); err != nil {
		return fmt.Errorf("failed to run startup migrations: %w", err)
	}
	config.SetDb(instance)
	if err := preserveLegacyHTTPSListen(); err != nil {
		return fmt.Errorf("preserve HTTPS listen: %w", err)
	}

	// 配置库就绪后、执行后续 AutoMigrate 与一次性 metrics 迁移之前：
	// 基于配置中的版本标记检测升级并自动备份 ./data，便于回滚。
	backupOnVersionUpgrade()

	// 自动迁移模型
	//
	// 注意：负载/GPU/ping 历史监控数据运行期全部走 metric store（默认 SQLite
	// ./data/metrics.db，或配置的 MySQL/PostgreSQL）。旧的 records /
	// records_long_term / gpu_records / ping_records 表不再建表、不再写入。
	// 若升级时旧表仍存在，会在 pkg/migrations.RunMetricStoreMigrations 中先导入再清理。
	// models.Record / models.PingRecord / models.GPURecord 结构体仍作为
	// metric store 的读写 DTO 和旧表导入 DTO 保留在 models 包中。

	copyLegacyReturnRouteNotify := instance.Migrator().HasTable("return_route_tasks") &&
		!instance.Migrator().HasColumn("return_route_tasks", "notify_recovery")
	err = instance.AutoMigrate(
		&models.User{},
		&models.Client{},
		&models.ClientDeploymentProfile{},
		&models.Log{},
		&models.Clipboard{},
		&models.LoadNotification{},
		&models.LoadNotificationState{},
		&models.MetricCleanupJob{},
		&models.OfflineNotification{},
		&models.TrafficReportNotification{},
		&models.TrafficDailyLedger{},
		&models.TrafficCalibrationAdjustment{},
		&models.PingTask{},
		&models.PingLossNotification{},
		&models.ReturnRouteTask{},
		&models.ReturnRouteStatus{},
		&models.ReturnRouteEvent{},
		&models.ReturnRouteProbeSample{},
		&models.ReturnRouteReachabilityStatus{},
		&models.OidcProvider{},
		&models.MessageSenderProvider{},
		&models.ThemeConfiguration{},
		&models.BillingPriceVersion{},
		&models.BillingFXSnapshot{},
		&models.BillingEntry{},
	)
	if err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}
	if err := billing.EnsureInitialPriceVersions(instance, time.Now().UTC()); err != nil {
		return fmt.Errorf("failed to initialize billing price versions: %w", err)
	}
	if err := billing.ReconcileStoredCurrencies(instance); err != nil {
		return fmt.Errorf("failed to reconcile billing currencies: %w", err)
	}
	if err := billing.ApplyUpgradeFX(context.Background(), instance, nil, ""); err != nil {
		return fmt.Errorf("failed to lock billing FX for upgrade: %w", err)
	}
	if copyLegacyReturnRouteNotify {
		if err := instance.Exec("UPDATE return_route_tasks SET notify_recovery = notify").Error; err != nil {
			return fmt.Errorf("failed to migrate return route notification settings: %w", err)
		}
	}
	if err := migrateLegacyReturnRouteLines(instance); err != nil {
		return fmt.Errorf("failed to migrate return route line names: %w", err)
	}
	if err := migrations.MigrateTrafficResetDayFromTags(instance); err != nil {
		return fmt.Errorf("failed to migrate traffic reset days: %w", err)
	}
	if err := instance.AutoMigrate(
		&models.Session{},
	); err != nil {
		logger.Errorf("dbcore", "Failed to create Session table, it may already exist: %v", err)
	}
	if err := instance.AutoMigrate(
		&models.Task{},
		&models.TaskResult{},
	); err != nil {
		logger.Errorf("dbcore", "Failed to create Task and TaskResult table, it may already exist: %v", err)
	}
	if err := cleanupOrphanedClientData(instance); err != nil {
		return fmt.Errorf("failed to clean orphaned client data: %w", err)
	}

	initialized = true
	return nil
}

func cleanupOrphanedPingLossNotifications(db *gorm.DB) error {
	if err := db.Where(`
		NOT EXISTS (
			SELECT 1 FROM clients
			WHERE clients.uuid = ping_loss_notifications.client
		)
		OR NOT EXISTS (
			SELECT 1 FROM ping_tasks
			WHERE ping_tasks.id = ping_loss_notifications.task_id
		)`,
	).Delete(&models.PingLossNotification{}).Error; err != nil {
		return err
	}

	var pingTasks []models.PingTask
	if err := db.Select("id", "clients").Find(&pingTasks).Error; err != nil {
		return fmt.Errorf("list ping tasks for notification reconciliation: %w", err)
	}
	assignedClients := make(map[uint]map[string]struct{}, len(pingTasks))
	for _, task := range pingTasks {
		clients := make(map[string]struct{}, len(task.Clients))
		for _, client := range task.Clients {
			clients[client] = struct{}{}
		}
		assignedClients[task.Id] = clients
	}

	var notifications []models.PingLossNotification
	if err := db.Select("id", "client", "task_id").Find(&notifications).Error; err != nil {
		return fmt.Errorf("list ping loss notifications for assignment reconciliation: %w", err)
	}
	staleIDs := make([]uint, 0)
	for _, notification := range notifications {
		if _, assigned := assignedClients[notification.TaskId][notification.Client]; !assigned {
			staleIDs = append(staleIDs, notification.Id)
		}
	}
	if len(staleIDs) == 0 {
		return nil
	}
	return db.Where("id IN ?", staleIDs).Delete(&models.PingLossNotification{}).Error
}

func cleanupOrphanedClientData(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable("return_route_tasks") {
			var orphanTaskIDs []uint
			if err := tx.Model(&models.ReturnRouteTask{}).Where(`NOT EXISTS (
				SELECT 1 FROM clients WHERE clients.uuid = return_route_tasks.client
			)`).Pluck("id", &orphanTaskIDs).Error; err != nil {
				return fmt.Errorf("list orphaned return route tasks: %w", err)
			}
			if len(orphanTaskIDs) > 0 {
				if err := tx.Where("task_id IN ?", orphanTaskIDs).Delete(&models.ReturnRouteEvent{}).Error; err != nil {
					return fmt.Errorf("delete orphaned return route events: %w", err)
				}
				if err := tx.Where("task_id IN ?", orphanTaskIDs).Delete(&models.ReturnRouteStatus{}).Error; err != nil {
					return fmt.Errorf("delete orphaned return route states: %w", err)
				}
				if err := tx.Where("task_id IN ?", orphanTaskIDs).Delete(&models.ReturnRouteProbeSample{}).Error; err != nil {
					return fmt.Errorf("delete orphaned return route samples: %w", err)
				}
				if err := tx.Where("id IN ?", orphanTaskIDs).Delete(&models.ReturnRouteTask{}).Error; err != nil {
					return fmt.Errorf("delete orphaned return route tasks: %w", err)
				}
			}
			if err := tx.Where(`NOT EXISTS (
				SELECT 1 FROM clients WHERE clients.uuid = return_route_reachability_statuses.client
			)`).Delete(&models.ReturnRouteReachabilityStatus{}).Error; err != nil {
				return fmt.Errorf("delete orphaned return route reachability: %w", err)
			}
		}
		for label, model := range map[string]any{
			"load notification states":        &models.LoadNotificationState{},
			"offline notifications":           &models.OfflineNotification{},
			"traffic report notifications":    &models.TrafficReportNotification{},
			"traffic daily ledger":            &models.TrafficDailyLedger{},
			"traffic calibration adjustments": &models.TrafficCalibrationAdjustment{},
		} {
			if !tx.Migrator().HasTable(model) {
				continue
			}
			if err := tx.Where(`NOT EXISTS (
				SELECT 1 FROM clients WHERE clients.uuid = client
			)`).Delete(model).Error; err != nil {
				return fmt.Errorf("delete orphaned %s: %w", label, err)
			}
		}
		if err := cleanupOrphanedPingLossNotifications(tx); err != nil {
			return err
		}
		if err := tx.Where(`
			NOT EXISTS (SELECT 1 FROM clients WHERE clients.uuid = task_results.client)
			OR NOT EXISTS (SELECT 1 FROM tasks WHERE tasks.task_id = task_results.task_id)
		`).Delete(&models.TaskResult{}).Error; err != nil {
			return fmt.Errorf("delete orphaned task results: %w", err)
		}

		var clients []models.Client
		if err := tx.Select("uuid").Find(&clients).Error; err != nil {
			return fmt.Errorf("list clients for orphan cleanup: %w", err)
		}
		validClients := make(map[string]struct{}, len(clients))
		for _, client := range clients {
			validClients[client.UUID] = struct{}{}
		}

		var pingTasks []models.PingTask
		if err := tx.Select("id", "clients").Find(&pingTasks).Error; err != nil {
			return fmt.Errorf("list ping tasks for orphan cleanup: %w", err)
		}
		for _, task := range pingTasks {
			remaining, changed := keepKnownClients(task.Clients, validClients)
			if changed {
				if err := tx.Model(&models.PingTask{}).Where("id = ?", task.Id).Update("clients", remaining).Error; err != nil {
					return fmt.Errorf("clean ping task %d clients: %w", task.Id, err)
				}
			}
		}

		var loadNotifications []models.LoadNotification
		if err := tx.Select("id", "clients").Find(&loadNotifications).Error; err != nil {
			return fmt.Errorf("list load notifications for orphan cleanup: %w", err)
		}
		if err := cleanupOrphanedLoadNotificationStates(tx, loadNotifications); err != nil {
			return err
		}
		for _, notification := range loadNotifications {
			remaining, changed := keepKnownClients(notification.Clients, validClients)
			if len(remaining) == 0 {
				if err := tx.Delete(&models.LoadNotification{}, notification.Id).Error; err != nil {
					return fmt.Errorf("delete empty load notification %d: %w", notification.Id, err)
				}
				continue
			}
			if !changed {
				continue
			}
			if err := tx.Model(&models.LoadNotification{}).Where("id = ?", notification.Id).Update("clients", remaining).Error; err != nil {
				return fmt.Errorf("clean load notification %d clients: %w", notification.Id, err)
			}
		}

		var commandTasks []models.Task
		if err := tx.Select("task_id", "clients").Find(&commandTasks).Error; err != nil {
			return fmt.Errorf("list command tasks for orphan cleanup: %w", err)
		}
		for _, task := range commandTasks {
			remaining, changed := keepKnownClients(task.Clients, validClients)
			if len(remaining) == 0 {
				if err := tx.Where("task_id = ?", task.TaskId).Delete(&models.TaskResult{}).Error; err != nil {
					return fmt.Errorf("delete empty command task %s results: %w", task.TaskId, err)
				}
				if err := tx.Where("task_id = ?", task.TaskId).Delete(&models.Task{}).Error; err != nil {
					return fmt.Errorf("delete empty command task %s: %w", task.TaskId, err)
				}
				continue
			}
			if !changed {
				continue
			}
			if err := tx.Model(&models.Task{}).Where("task_id = ?", task.TaskId).Update("clients", remaining).Error; err != nil {
				return fmt.Errorf("clean command task %s clients: %w", task.TaskId, err)
			}
		}

		if err := billing.CloseOrphanedPriceVersions(tx, time.Now()); err != nil {
			return fmt.Errorf("close orphaned billing versions: %w", err)
		}

		for _, table := range []string{"records", "records_long_term", "gpu_records", "ping_records"} {
			if !tx.Migrator().HasTable(table) {
				continue
			}
			predicate := fmt.Sprintf(`NOT EXISTS (
				SELECT 1 FROM clients WHERE clients.uuid = %s.client
			)`, table)
			if table == "ping_records" {
				predicate += ` OR NOT EXISTS (
					SELECT 1 FROM ping_tasks WHERE ping_tasks.id = ping_records.task_id
				)`
			}
			query := "DELETE FROM " + table + " WHERE " + predicate
			if err := tx.Exec(query).Error; err != nil {
				return fmt.Errorf("delete orphaned rows from legacy table %s: %w", table, err)
			}
		}
		return nil
	})
}

func cleanupOrphanedLoadNotificationStates(db *gorm.DB, notifications []models.LoadNotification) error {
	if !db.Migrator().HasTable(&models.LoadNotificationState{}) {
		return nil
	}
	assigned := make(map[uint]map[string]struct{}, len(notifications))
	for _, notification := range notifications {
		clients := make(map[string]struct{}, len(notification.Clients))
		for _, client := range notification.Clients {
			clients[client] = struct{}{}
		}
		assigned[notification.Id] = clients
	}
	var states []models.LoadNotificationState
	if err := db.Select("notification_id", "client").Find(&states).Error; err != nil {
		return fmt.Errorf("list load notification states for reconciliation: %w", err)
	}
	for _, state := range states {
		if _, ok := assigned[state.NotificationID][state.Client]; ok {
			continue
		}
		if err := db.Where("notification_id = ? AND client = ?", state.NotificationID, state.Client).
			Delete(&models.LoadNotificationState{}).Error; err != nil {
			return fmt.Errorf("delete orphaned load notification state: %w", err)
		}
	}
	return nil
}

func keepKnownClients(clients models.StringArray, valid map[string]struct{}) (models.StringArray, bool) {
	remaining := make(models.StringArray, 0, len(clients))
	changed := false
	for _, client := range clients {
		if _, ok := valid[client]; !ok {
			changed = true
			continue
		}
		remaining = append(remaining, client)
	}
	return remaining, changed
}
