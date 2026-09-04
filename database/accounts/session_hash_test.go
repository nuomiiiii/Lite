package accounts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nuomiiiii/lite/utils/instancekey"
)

func TestSessionHashIsNotTheCookie(t *testing.T) {
	t.Setenv("LITE_INSTANCE_KEY_FILE", filepath.Join(t.TempDir(), "lite-instance.key"))
	instancekey.ResetForTest()
	plain := "abcdefghijklmnopqrstuvwxyzabcdef"
	hashed, err := HashSessionToken(plain)
	if err != nil {
		t.Fatal(err)
	}
	if hashed == plain {
		t.Fatal("session hash leaked the cookie value")
	}
	if SessionLookupKey(plain) != hashed {
		t.Fatal("lookup key should hash plaintext cookies")
	}
	if SessionLookupKey(hashed) != hashed {
		t.Fatal("lookup key should keep hashed identifiers")
	}
	if _, err := os.Stat(instancekey.Path()); err != nil {
		t.Fatalf("instance key was not created: %v", err)
	}
}
