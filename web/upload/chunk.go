// Package upload provides the shared chunked archive upload flow.
package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/komari-monitor/komari/web/backup"
)

const (
	ChunkSize              int64 = 5 * 1024 * 1024
	defaultMaxReservedSize int64 = 8 << 30
	defaultMaxSessions           = 2
	defaultSessionTTL            = 24 * time.Hour
)

type Purpose string

const (
	PurposeBackup Purpose = "backup"
	PurposeTheme  Purpose = "theme"
)

var (
	ErrNotFound       = errors.New("upload not found or expired")
	ErrOwnerMismatch  = errors.New("upload does not belong to this requester")
	ErrTooManyUploads = errors.New("too many uploads are already in progress")
	ErrStorageLimit   = errors.New("temporary upload storage limit reached")
)

type Metadata struct {
	Purpose   Purpose   `json:"purpose"`
	Size      int64     `json:"size"`
	Filename  string    `json:"filename"`
	Owner     string    `json:"owner"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Session struct {
	ID          string
	Metadata    Metadata
	Directory   string
	ArchivePath string
}

type Store struct {
	Root                string
	MaxSize             int64
	MaxReservedSize     int64
	MaxSessionsPerOwner int
	SessionTTL          time.Duration
	Now                 func() time.Time
	mu                  sync.Mutex
}

var DefaultStore = &Store{
	Root:                filepath.Join(".", "data", ".uploading"),
	MaxSize:             backup.MaxArchiveSize,
	MaxReservedSize:     defaultMaxReservedSize,
	MaxSessionsPerOwner: defaultMaxSessions,
	SessionTTL:          defaultSessionTTL,
	Now:                 time.Now,
}

func (s *Store) Init(owner string, purpose Purpose, filename string, size, purposeMaxSize int64) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Session{}, errors.New("upload owner is required")
	}
	if !isKnownPurpose(purpose) {
		return Session{}, errors.New("invalid upload purpose")
	}
	maxSize := s.MaxSize
	if purposeMaxSize > 0 && purposeMaxSize < maxSize {
		maxSize = purposeMaxSize
	}
	if size <= 0 || size > maxSize {
		return Session{}, fmt.Errorf("size must be between 1 and %d bytes", maxSize)
	}
	filename = safeFilename(filename)
	if filename == "" {
		return Session{}, errors.New("filename is required")
	}
	if err := s.cleanupExpiredLocked(); err != nil {
		return Session{}, err
	}

	sessions, err := s.sessionsLocked()
	if err != nil {
		return Session{}, err
	}
	ownerSessions := 0
	var reserved int64
	for _, session := range sessions {
		reserved += session.Metadata.Size
		if session.Metadata.Owner == owner {
			ownerSessions++
		}
	}
	if ownerSessions >= s.maxSessionsPerOwner() {
		return Session{}, ErrTooManyUploads
	}
	if size > s.maxReservedSize()-reserved {
		return Session{}, ErrStorageLimit
	}

	id := uuid.NewString()
	directory := filepath.Join(s.Root, id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Session{}, fmt.Errorf("create upload directory: %w", err)
	}
	metadata := Metadata{
		Purpose: purpose, Size: size, Filename: filename, Owner: owner,
		UpdatedAt: s.now().UTC(),
	}
	if err := writeMetadata(directory, metadata); err != nil {
		_ = os.RemoveAll(directory)
		return Session{}, err
	}
	return Session{ID: id, Metadata: metadata, Directory: directory}, nil
}

func (s *Store) SaveChunk(owner, uploadID string, index int64, source io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.loadLocked(owner, uploadID)
	if err != nil {
		return err
	}
	expectedSize, err := expectedChunkSize(session.Metadata.Size, index)
	if err != nil {
		return err
	}

	temporary, err := os.CreateTemp(session.Directory, fmt.Sprintf(".%d-*.part", index))
	if err != nil {
		return fmt.Errorf("create chunk: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	written, copyErr := io.Copy(temporary, io.LimitReader(source, expectedSize+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return fmt.Errorf("write chunk: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close chunk: %w", closeErr)
	}
	if written != expectedSize {
		return fmt.Errorf("chunk %d has size %d, want %d", index, written, expectedSize)
	}

	chunkPath := filepath.Join(session.Directory, chunkFilename(index))
	if err := os.Remove(chunkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace chunk: %w", err)
	}
	if err := os.Rename(temporaryPath, chunkPath); err != nil {
		return fmt.Errorf("publish chunk: %w", err)
	}
	session.Metadata.UpdatedAt = s.now().UTC()
	return writeMetadata(session.Directory, session.Metadata)
}

// MergeAndFinalize holds the store lock through finalization so cancel and
// duplicate merge requests cannot remove or apply the same archive midway.
func (s *Store) MergeAndFinalize(owner, uploadID string, finalize Finalizer) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := s.mergeLocked(owner, uploadID)
	if err != nil {
		_ = s.cancelLocked(owner, uploadID)
		return Result{}, err
	}
	defer os.RemoveAll(session.Directory)
	return finalize(session)
}

func (s *Store) mergeLocked(owner, uploadID string) (Session, error) {
	session, err := s.loadLocked(owner, uploadID)
	if err != nil {
		return Session{}, err
	}
	temporary, err := os.CreateTemp(session.Directory, ".merged-*.zip")
	if err != nil {
		return Session{}, fmt.Errorf("create merged archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()

	for index := int64(0); index < chunkCount(session.Metadata.Size); index++ {
		chunkPath := filepath.Join(session.Directory, chunkFilename(index))
		info, err := os.Stat(chunkPath)
		if err != nil {
			if os.IsNotExist(err) {
				return Session{}, fmt.Errorf("chunk %d is missing", index)
			}
			return Session{}, fmt.Errorf("read chunk %d: %w", index, err)
		}
		expectedSize, _ := expectedChunkSize(session.Metadata.Size, index)
		if info.Size() != expectedSize {
			return Session{}, fmt.Errorf("chunk %d has size %d, want %d", index, info.Size(), expectedSize)
		}
		chunk, err := os.Open(chunkPath)
		if err != nil {
			return Session{}, fmt.Errorf("open chunk %d: %w", index, err)
		}
		_, copyErr := io.Copy(temporary, chunk)
		closeErr := chunk.Close()
		if copyErr != nil {
			return Session{}, fmt.Errorf("merge chunk %d: %w", index, copyErr)
		}
		if closeErr != nil {
			return Session{}, fmt.Errorf("close chunk %d: %w", index, closeErr)
		}
	}
	if err := temporary.Sync(); err != nil {
		return Session{}, fmt.Errorf("sync merged archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Session{}, fmt.Errorf("close merged archive: %w", err)
	}
	archivePath := filepath.Join(session.Directory, "archive.zip")
	if err := os.Rename(temporaryPath, archivePath); err != nil {
		return Session{}, fmt.Errorf("publish merged archive: %w", err)
	}
	session.ArchivePath = archivePath
	return session, nil
}

func (s *Store) Cancel(owner, uploadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelLocked(owner, uploadID)
}

func (s *Store) cancelLocked(owner, uploadID string) error {
	if !validUploadID(uploadID) {
		return errors.New("invalid upload id")
	}
	session, err := s.loadLocked(owner, uploadID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.RemoveAll(session.Directory); err != nil {
		return fmt.Errorf("remove upload: %w", err)
	}
	return nil
}

func (s *Store) CleanupExpired() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupExpiredLocked()
}

// CleanupAll removes interrupted sessions from a previous process. Browsers
// cannot safely resume them after a server restart because authentication and
// finalization state may have changed.
func (s *Store) CleanupAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read upload directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validUploadID(entry.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.Root, entry.Name())); err != nil {
			return fmt.Errorf("remove interrupted upload: %w", err)
		}
	}
	return nil
}

func (s *Store) cleanupExpiredLocked() error {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read upload directory: %w", err)
	}
	cutoff := s.now().Add(-s.sessionTTL())
	for _, entry := range entries {
		if !entry.IsDir() || !validUploadID(entry.Name()) {
			continue
		}
		directory := filepath.Join(s.Root, entry.Name())
		metadata, err := readMetadata(directory)
		if err != nil || metadata.UpdatedAt.Before(cutoff) {
			if removeErr := os.RemoveAll(directory); removeErr != nil {
				return fmt.Errorf("remove expired upload: %w", removeErr)
			}
		}
	}
	return nil
}

func (s *Store) sessionsLocked() ([]Session, error) {
	entries, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read upload directory: %w", err)
	}
	sessions := make([]Session, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validUploadID(entry.Name()) {
			continue
		}
		directory := filepath.Join(s.Root, entry.Name())
		metadata, err := readMetadata(directory)
		if err != nil {
			continue
		}
		sessions = append(sessions, Session{ID: entry.Name(), Metadata: metadata, Directory: directory})
	}
	return sessions, nil
}

func (s *Store) loadLocked(owner, uploadID string) (Session, error) {
	if !validUploadID(uploadID) {
		return Session{}, errors.New("invalid upload id")
	}
	directory := filepath.Join(s.Root, uploadID)
	metadata, err := readMetadata(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	if metadata.Owner != owner {
		return Session{}, ErrOwnerMismatch
	}
	if metadata.UpdatedAt.Before(s.now().Add(-s.sessionTTL())) {
		_ = os.RemoveAll(directory)
		return Session{}, ErrNotFound
	}
	if !isKnownPurpose(metadata.Purpose) || metadata.Size <= 0 || metadata.Size > s.MaxSize {
		return Session{}, errors.New("invalid upload metadata")
	}
	return Session{ID: uploadID, Metadata: metadata, Directory: directory}, nil
}

func writeMetadata(directory string, metadata Metadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode upload metadata: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".upload-*.json")
	if err != nil {
		return fmt.Errorf("create upload metadata: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure upload metadata: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write upload metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close upload metadata: %w", err)
	}
	destination := filepath.Join(directory, "upload.json")
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace upload metadata: %w", err)
	}
	if err := os.Rename(name, destination); err != nil {
		return fmt.Errorf("publish upload metadata: %w", err)
	}
	return nil
}

func readMetadata(directory string) (Metadata, error) {
	data, err := os.ReadFile(filepath.Join(directory, "upload.json"))
	if err != nil {
		return Metadata{}, err
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("read upload metadata: %w", err)
	}
	return metadata, nil
}

func safeFilename(filename string) string {
	filename = strings.ReplaceAll(filename, `\`, "/")
	filename = path.Base(filename)
	filename = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, filename)
	filename = strings.TrimSpace(filename)
	if filename == "." || filename == ".." {
		return ""
	}
	const maxFilenameBytes = 240
	for len(filename) > maxFilenameBytes {
		_, size := utf8.DecodeLastRuneInString(filename)
		filename = filename[:len(filename)-size]
	}
	return filename
}

func isKnownPurpose(purpose Purpose) bool {
	return purpose == PurposeBackup || purpose == PurposeTheme
}

func validUploadID(uploadID string) bool {
	parsed, err := uuid.Parse(uploadID)
	return err == nil && parsed.String() == strings.ToLower(uploadID)
}

func chunkCount(size int64) int64 {
	return (size + ChunkSize - 1) / ChunkSize
}

func expectedChunkSize(size, index int64) (int64, error) {
	if size <= 0 || index < 0 || index >= chunkCount(size) {
		return 0, errors.New("invalid chunk index")
	}
	if index == chunkCount(size)-1 {
		return size - index*ChunkSize, nil
	}
	return ChunkSize, nil
}

func chunkFilename(index int64) string {
	return strconv.FormatInt(index, 10) + ".part"
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) sessionTTL() time.Duration {
	if s.SessionTTL > 0 {
		return s.SessionTTL
	}
	return defaultSessionTTL
}

func (s *Store) maxReservedSize() int64 {
	if s.MaxReservedSize > 0 {
		return s.MaxReservedSize
	}
	return defaultMaxReservedSize
}

func (s *Store) maxSessionsPerOwner() int {
	if s.MaxSessionsPerOwner > 0 {
		return s.MaxSessionsPerOwner
	}
	return defaultMaxSessions
}
