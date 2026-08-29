package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTestArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func TestReplaceStagedBackupUsesCompleteFilesOnWindowsStyleReplacement(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "backup.zip")
	stagedPath := filepath.Join(dir, ".backup-upload.zip")
	if err := os.WriteFile(finalPath, []byte("previous-complete-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagedPath, []byte("new-complete-archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	rename := func(oldPath, newPath string) error {
		calls++
		if calls == 1 {
			return os.ErrExist
		}
		return os.Rename(oldPath, newPath)
	}
	if err := replaceStagedBackup(stagedPath, finalPath, rename); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-complete-archive" {
		t.Fatalf("final backup = %q", got)
	}
}

func TestReplaceStagedBackupRestoresPreviousFileWhenInstallFails(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "backup.zip")
	stagedPath := filepath.Join(dir, ".backup-upload.zip")
	if err := os.WriteFile(finalPath, []byte("previous-complete-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagedPath, []byte("new-complete-archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	rename := func(oldPath, newPath string) error {
		calls++
		switch calls {
		case 1:
			return os.ErrExist
		case 3:
			return errors.New("replacement blocked")
		default:
			return os.Rename(oldPath, newPath)
		}
	}
	if err := replaceStagedBackup(stagedPath, finalPath, rename); err == nil {
		t.Fatal("replacement failure was not returned")
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "previous-complete-archive" {
		t.Fatalf("restored backup = %q", got)
	}
	staged, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(staged) != "new-complete-archive" {
		t.Fatalf("staged backup = %q", staged)
	}
}

func TestValidateArchiveAcceptsFullAndConfigurationPackages(t *testing.T) {
	for name, entries := range map[string]map[string]string{
		"config": {
			"lite.db":            "main",
			"lite-backup-markup": "config",
		},
		"full": {
			"lite.db":            "main",
			"metrics.db":         "history",
			"lite-backup-markup": "full",
		},
		"legacy komari markup": {
			"lite.db":              "main",
			"komari-backup-markup": "config",
		},
		"komari lite": {
			"komari.db":            "main",
			"komari-backup-markup": "config",
		},
		"komari 1.4 full": {
			"komari.db":            "main",
			"metrics.db":           "history",
			"komari-backup-markup": "full",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateArchive(writeTestArchive(t, entries)); err != nil {
				t.Fatalf("ValidateArchive rejected package: %v", err)
			}
		})
	}
}

func TestValidateArchiveRejectsPackagesThatCouldWipeCurrentData(t *testing.T) {
	for name, entries := range map[string]map[string]string{
		"missing database": {"lite-backup-markup": "marker"},
		"missing marker":   {"lite.db": "main"},
		"path traversal": {
			"lite.db":            "main",
			"lite-backup-markup": "marker",
			"../outside":         "bad",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateArchive(writeTestArchive(t, entries)); err == nil {
				t.Fatal("ValidateArchive accepted unsafe package")
			}
		})
	}
}
