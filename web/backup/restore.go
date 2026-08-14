// Package backup validates and stages Komari backup archives for startup restore.
package backup

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

var restoreMutex sync.Mutex

const (
	MaxArchiveSize    int64 = 4 << 30
	maxArchiveEntries       = 100_000
)

// RestoreLock prevents a second upload from replacing the already validated
// backup while the first request is waiting for the process to restart.
type RestoreLock struct {
	once sync.Once
}

func AcquireRestoreLock() (*RestoreLock, error) {
	if !restoreMutex.TryLock() {
		return nil, fmt.Errorf("another restore operation is already in progress")
	}
	return &RestoreLock{}, nil
}

func (l *RestoreLock) Release() {
	l.once.Do(restoreMutex.Unlock)
}

func SaveUploadedBackup(file io.Reader, filename string) error {
	lock, err := AcquireRestoreLock()
	if err != nil {
		return err
	}
	defer lock.Release()
	return lock.SaveUploadedBackup(file, filename)
}

func (l *RestoreLock) SaveUploadedBackup(file io.Reader, filename string) error {
	if !strings.HasSuffix(strings.ToLower(filename), ".zip") {
		return fmt.Errorf("uploaded file must be a ZIP archive")
	}
	if err := os.MkdirAll("./data", 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	tempFile, err := os.CreateTemp("./data", ".backup-upload-*.zip")
	if err != nil {
		return fmt.Errorf("create temporary backup: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	written, err := io.Copy(tempFile, io.LimitReader(file, MaxArchiveSize+1))
	if err != nil {
		tempFile.Close()
		return fmt.Errorf("save uploaded backup: %w", err)
	}
	if written > MaxArchiveSize {
		tempFile.Close()
		return fmt.Errorf("backup archive exceeds the %d byte limit", MaxArchiveSize)
	}
	if err := tempFile.Sync(); err != nil {
		tempFile.Close()
		return fmt.Errorf("sync uploaded backup: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close uploaded backup: %w", err)
	}
	if err := ValidateArchive(tempPath); err != nil {
		return err
	}

	finalPath := filepath.Join(".", "data", "backup.zip")
	return replaceStagedBackup(tempPath, finalPath, os.Rename)
}

type renameFileFunc func(oldPath, newPath string) error

// replaceStagedBackup keeps both the staged and previous archive complete.
// Unix can replace the destination directly; Windows needs the existing file
// moved aside first, with an immediate rollback if the second rename fails.
func replaceStagedBackup(stagedPath, finalPath string, renameFile renameFileFunc) error {
	if err := renameFile(stagedPath, finalPath); err == nil {
		return nil
	} else if _, statErr := os.Stat(finalPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return fmt.Errorf("install staged backup: %w", err)
		}
		return fmt.Errorf("inspect previous backup: %w", statErr)
	}

	previousFile, err := os.CreateTemp(filepath.Dir(finalPath), ".backup-previous-*.zip")
	if err != nil {
		return fmt.Errorf("reserve previous backup path: %w", err)
	}
	previousPath := previousFile.Name()
	if err := previousFile.Close(); err != nil {
		os.Remove(previousPath)
		return fmt.Errorf("close previous backup placeholder: %w", err)
	}
	if err := os.Remove(previousPath); err != nil {
		return fmt.Errorf("prepare previous backup path: %w", err)
	}
	defer os.Remove(previousPath)

	if err := renameFile(finalPath, previousPath); err != nil {
		return fmt.Errorf("preserve previous backup: %w", err)
	}
	if err := renameFile(stagedPath, finalPath); err != nil {
		if restoreErr := renameFile(previousPath, finalPath); restoreErr != nil {
			return fmt.Errorf("install staged backup: %v; restore previous backup: %w", err, restoreErr)
		}
		return fmt.Errorf("install staged backup: %w", err)
	}
	return nil
}

func normalizedArchivePath(name string) (string, error) {
	if name == "" || strings.Contains(name, `\`) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid backup path %q", name)
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid backup path %q", name)
	}
	return cleaned, nil
}

// ValidateArchive requires the upstream-compatible root layout and bounds all
// archive entries before the startup path is allowed to replace current data.
func ValidateArchive(archivePath string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open backup archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("backup archive has too many files: %d", len(reader.File))
	}

	var expandedSize uint64
	hasMarkup := false
	hasMainDatabase := false
	seen := make(map[string]struct{}, len(reader.File))
	for _, entry := range reader.File {
		name, err := normalizedArchivePath(entry.Name)
		if err != nil {
			return err
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("backup archive contains duplicate path %q", name)
		}
		seen[name] = struct{}{}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup archive contains unsupported symbolic link %q", name)
		}
		if name == "komari-backup-markup" {
			hasMarkup = true
		}
		if name == "komari.db" && !entry.FileInfo().IsDir() {
			hasMainDatabase = true
		}
		if entry.UncompressedSize64 > uint64(MaxArchiveSize) || expandedSize > uint64(MaxArchiveSize)-entry.UncompressedSize64 {
			return fmt.Errorf("backup archive expands beyond the %d byte limit", MaxArchiveSize)
		}
		expandedSize += entry.UncompressedSize64
	}
	if !hasMarkup {
		return fmt.Errorf("invalid backup file: missing komari-backup-markup file")
	}
	if !hasMainDatabase {
		return fmt.Errorf("invalid backup file: missing komari.db")
	}
	return nil
}
