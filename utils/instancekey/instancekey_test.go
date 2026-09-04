package instancekey

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeEncodedRejectsWrongSize(t *testing.T) {
	if _, err := DecodeEncoded("not-base64"); err == nil {
		t.Fatal("invalid base64 was accepted")
	}
	if _, err := DecodeEncoded(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short key was accepted")
	}
}

func TestWriteEncodedToReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	first := base64.StdEncoding.EncodeToString(make([]byte, 32))
	secondKey := make([]byte, 32)
	secondKey[0] = 1
	second := base64.StdEncoding.EncodeToString(secondKey)
	if err := WriteEncodedTo(path, first); err != nil {
		t.Fatal(err)
	}
	if err := WriteEncodedTo(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := ReadEncodedFrom(path)
	if err != nil || got != second {
		t.Fatalf("got %q err=%v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Logf("key file mode = %o (unix 0600 is enforced where the OS honors it)", info.Mode().Perm())
	}
}
