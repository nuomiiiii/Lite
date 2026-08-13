package upload

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{
		Root:                filepath.Join(t.TempDir(), "uploading"),
		MaxSize:             32 << 20,
		MaxReservedSize:     64 << 20,
		MaxSessionsPerOwner: 2,
		SessionTTL:          time.Hour,
		Now:                 time.Now,
	}
}

func TestStoreUploadsAndMergesMultipleChunks(t *testing.T) {
	store := testStore(t)
	size := ChunkSize + 7
	session, err := store.Init("admin:user-1", PurposeBackup, "backup.zip", size, 0)
	if err != nil {
		t.Fatalf("init upload: %v", err)
	}
	first := bytes.Repeat([]byte("a"), int(ChunkSize))
	if err := store.SaveChunk("admin:user-1", session.ID, 0, bytes.NewReader(first)); err != nil {
		t.Fatalf("save first chunk: %v", err)
	}
	if err := store.SaveChunk("admin:user-1", session.ID, 1, strings.NewReader("1234567")); err != nil {
		t.Fatalf("save final chunk: %v", err)
	}

	var merged []byte
	result, err := store.MergeAndFinalize("admin:user-1", session.ID, func(upload Session) (Result, error) {
		var readErr error
		merged, readErr = os.ReadFile(upload.ArchivePath)
		return Result{Message: "ok"}, readErr
	})
	if err != nil {
		t.Fatalf("merge upload: %v", err)
	}
	if result.Message != "ok" || len(merged) != int(size) || !bytes.Equal(merged[:len(first)], first) || string(merged[len(first):]) != "1234567" {
		t.Fatal("merged archive content does not match uploaded chunks")
	}
	if _, err := os.Stat(session.Directory); !os.IsNotExist(err) {
		t.Fatalf("completed session was not removed: %v", err)
	}
}

func TestStoreRejectsInvalidChunkAndCleansFailedMerge(t *testing.T) {
	store := testStore(t)
	session, err := store.Init("install", PurposeBackup, "backup.zip", ChunkSize+3, 0)
	if err != nil {
		t.Fatalf("init upload: %v", err)
	}
	if err := store.SaveChunk("install", session.ID, 0, strings.NewReader("short")); err == nil {
		t.Fatal("undersized chunk was accepted")
	}
	if err := store.SaveChunk("install", session.ID, 2, strings.NewReader("bad")); err == nil {
		t.Fatal("out-of-range chunk was accepted")
	}
	if _, err := store.MergeAndFinalize("install", session.ID, func(Session) (Result, error) {
		return Result{}, nil
	}); err == nil || !strings.Contains(err.Error(), "chunk 0 is missing") {
		t.Fatalf("merge missing chunk error = %v", err)
	}
	if _, err := os.Stat(session.Directory); !os.IsNotExist(err) {
		t.Fatalf("failed merge session was not removed: %v", err)
	}
}

func TestStoreBindsSessionsToOwner(t *testing.T) {
	store := testStore(t)
	session, err := store.Init("admin:user-1", PurposeBackup, "backup.zip", 3, 0)
	if err != nil {
		t.Fatalf("init upload: %v", err)
	}
	if err := store.SaveChunk("admin:user-2", session.ID, 0, strings.NewReader("abc")); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("wrong owner error = %v, want ErrOwnerMismatch", err)
	}
	if err := store.Cancel("admin:user-2", session.ID); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("wrong owner cancel error = %v, want ErrOwnerMismatch", err)
	}
	if err := store.SaveChunk("admin:user-1", session.ID, 0, strings.NewReader("abc")); err != nil {
		t.Fatalf("right owner could not upload: %v", err)
	}
}

func TestStoreLimitsSessionsAndReservedSpace(t *testing.T) {
	store := testStore(t)
	store.MaxSessionsPerOwner = 1
	if _, err := store.Init("admin", PurposeBackup, "one.zip", 4, 0); err != nil {
		t.Fatalf("init first upload: %v", err)
	}
	if _, err := store.Init("admin", PurposeBackup, "two.zip", 4, 0); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("second owner session error = %v, want ErrTooManyUploads", err)
	}
	store.MaxSessionsPerOwner = 2
	store.MaxReservedSize = 6
	if _, err := store.Init("other", PurposeBackup, "three.zip", 3, 0); !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("reserved space error = %v, want ErrStorageLimit", err)
	}
}

func TestStoreExpiresAndCleansInterruptedSessions(t *testing.T) {
	store := testStore(t)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }
	session, err := store.Init("install", PurposeBackup, "backup.zip", 3, 0)
	if err != nil {
		t.Fatalf("init upload: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if err := store.SaveChunk("install", session.ID, 0, strings.NewReader("abc")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session error = %v, want ErrNotFound", err)
	}

	second, err := store.Init("install", PurposeBackup, "backup.zip", 3, 0)
	if err != nil {
		t.Fatalf("init second upload: %v", err)
	}
	if err := store.CleanupAll(); err != nil {
		t.Fatalf("cleanup all: %v", err)
	}
	if _, err := os.Stat(second.Directory); !os.IsNotExist(err) {
		t.Fatalf("interrupted session was not removed: %v", err)
	}
}

func TestStoreSanitizesFilenameAndPurposeLimit(t *testing.T) {
	store := testStore(t)
	session, err := store.Init("admin", PurposeTheme, "../../theme.zip", 4, 8)
	if err != nil {
		t.Fatalf("init theme upload: %v", err)
	}
	if session.Metadata.Filename != "theme.zip" {
		t.Fatalf("sanitized filename = %q, want theme.zip", session.Metadata.Filename)
	}
	if _, err := store.Init("admin", PurposeTheme, "large.zip", 9, 8); err == nil {
		t.Fatal("purpose-specific size limit was ignored")
	}
}
