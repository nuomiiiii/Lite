package accounts

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func TestArgon2idPasswordRoundTripAndLegacy(t *testing.T) {
	encoded, err := hashPasswd("LiteTest9a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, argonPrefix) {
		t.Fatalf("new hash = %q", encoded)
	}
	if !strings.Contains(encoded, "m=32768,t=3,p=1") {
		t.Fatalf("new hash params = %q, want m=32768,t=3,p=1", encoded)
	}
	if !verifyPasswd("LiteTest9a", encoded) {
		t.Fatal("argon2id verify failed")
	}
	if verifyPasswd("wrong", encoded) {
		t.Fatal("wrong password accepted")
	}
	legacy := hashLegacySHA256("LiteTest9a")
	if !isLegacyPasswordHash(legacy) {
		t.Fatal("legacy hash not detected")
	}
	if !verifyPasswd("LiteTest9a", legacy) {
		t.Fatal("legacy password rejected")
	}
}

func TestTOTPCounterReplayAndNextWindow(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Lite", AccountName: "test"})
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
