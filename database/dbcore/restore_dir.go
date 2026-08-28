package dbcore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// renameDirectory is the directory-level rename used when swapping data/.
// Tests replace it to simulate Docker bind mounts, which cannot be renamed.
var renameDirectory = os.Rename

func shouldFallbackDirectoryMove(err error) bool {
	if err == nil {
		return false
	}
	var link *os.LinkError
	if errors.As(err, &link) {
		err = link.Err
	}
	if errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV) || errors.Is(err, os.ErrExist) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "busy") ||
		strings.Contains(msg, "file exists") ||
		strings.Contains(msg, "not empty") ||
		strings.Contains(msg, "cannot replace")
}

// relocateDirectory moves src to dest. If src is a mount point (Docker volume)
// or dest already exists, it moves the directory entries instead of renaming
// the directory inode.
func relocateDirectory(src, dest string) error {
	err := renameDirectory(src, dest)
	if err == nil {
		return nil
	}
	if !shouldFallbackDirectoryMove(err) {
		return err
	}

	destExists := false
	info, statErr := os.Stat(dest)
	if statErr == nil {
		if !info.IsDir() {
			return err
		}
		destExists = true
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if !destExists {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
	}
	if err := moveDirectoryEntries(src, dest); err != nil {
		return err
	}
	if !destExists {
		return nil
	}
	if removeErr := os.RemoveAll(src); removeErr != nil && !os.IsNotExist(removeErr) && !shouldFallbackDirectoryMove(removeErr) {
		return removeErr
	}
	return nil
}

func moveDirectoryEntries(src, dest string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dest, entry.Name())
		if err := movePath(from, to); err != nil {
			return err
		}
	}
	return nil
}

func movePath(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	} else if !shouldFallbackDirectoryMove(err) {
		return err
	}
	if err := copyPath(src, dest); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyPath(src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.Remove(dest)
		return os.Symlink(target, dest)
	}
	if info.IsDir() {
		if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
			return err
		}
		return moveDirectoryEntries(src, dest)
	}
	return copyFile(src, dest, info.Mode().Perm())
}

func copyFile(src, dest string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
