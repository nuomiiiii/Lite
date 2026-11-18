package accounts

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func TestArgon2idPasswordRoundTripAndLegacy(t *testing.T) {
	encoded, err := hashPasswd("correctpassword")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, argonPrefix) {
		t.Fatalf("new hash = %q", encoded)
	}
	if !strings.Contains(encoded, "m=32768,t=3,p=1") {
		t.Fatalf("new hash params = %q, want m=32768,t=3,p=1", encoded)
	}
	if !verifyPasswd("correctpassword", encoded) {
		t.Fatal("argon2id verify failed")
	}
	if verifyPasswd("wrong", encoded) {
		t.Fatal("wrong password accepted")
	}
	legacy := hashLegacySHA256("correctpassword")
	if !isLegacyPasswordHash(legacy) {
		t.Fatal("legacy hash not detected")
	}
	if !verifyPasswd("correctpassword", legacy) {
		t.Fatal("legacy password rejected")
	}
}

func TestTOTPCounterReplayAndNextWindow(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: TwoFactorIssuer, AccountName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := totp.GenerateCodeCustom(key.Secret(), now, totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	counter, ok := matchingTOTPCounter(key.Secret(), code, 0, now)
	if !ok {
		t.Fatal("valid TOTP was rejected")
	}
	if _, ok := matchingTOTPCounter(key.Secret(), code, counter, now); ok {
		t.Fatal("replayed TOTP was accepted")
	}
	nextTime := now.Add(45 * time.Second)
	nextCode, err := totp.GenerateCodeCustom(key.Secret(), nextTime, totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := matchingTOTPCounter(key.Secret(), nextCode, counter, nextTime); !ok {
		t.Fatal("next window TOTP was rejected")
	}
}

func TestEnable2FaRejectsOverwriteWhenAlreadyEnabled(t *testing.T) {
	username := "totp-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "correctpassword")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	secret1, _, err := Generate2Fa()
	if err != nil {
		t.Fatal(err)
	}
	code1, err := totp.GenerateCode(secret1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Enable2Fa(user.UUID, secret1, code1); err != nil {
		t.Fatalf("first Enable2Fa failed: %v", err)
	}

	secret2, _, err := Generate2Fa()
	if err != nil {
		t.Fatal(err)
	}
	code2, err := totp.GenerateCode(secret2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Enable2Fa(user.UUID, secret2, code2); !errors.Is(err, ErrTwoFactorAlreadyEnabled) {
		t.Fatalf("second Enable2Fa error = %v, want ErrTwoFactorAlreadyEnabled", err)
	}

	ok, err := Verify2Fa(user.UUID, code2)
	if err != nil || ok {
		t.Fatalf("second secret should not verify: ok=%v err=%v", ok, err)
	}
}
