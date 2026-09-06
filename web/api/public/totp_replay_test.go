package public

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/web/remotectl"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func TestTOTPReplayIsSharedAcrossLoginAndRemoteReauth(t *testing.T) {
	user, err := accounts.CreateAccount("tx-"+uuid.NewString()[:8], "correctpassword")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accounts.DeleteAccountByUsername(user.Username) })

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Lite", AccountName: user.Username})
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.Enable2Fa(user.UUID, key.Secret(), code); err != nil {
		t.Fatal(err)
	}
	if err := dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", user.UUID).
		Update("two_factor_counter", 0).Error; err != nil {
		t.Fatal(err)
	}
	live, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}

	remotectl.ResetForTest()
	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ok, verifyErr := accounts.Verify2Fa(user.UUID, live)
		if verifyErr != nil {
			results[0] = verifyErr
			return
		}
		if !ok {
			results[0] = errTOTPRejected
		}
	}()
	go func() {
		defer wg.Done()
		results[1] = remotectl.Reauthorize(user.UUID, "", live, "127.0.0.1")
	}()
	wg.Wait()
	successes := 0
	for _, result := range results {
		if result == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("cross-entry TOTP successes = %d (%v), want 1", successes, results)
	}
}

func TestReauthorizeSSOWithoutLocalPassword(t *testing.T) {
	user, err := accounts.CreateAccount("sso-"+uuid.NewString()[:8], "unused-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accounts.DeleteAccountByUsername(user.Username) })
	if err := dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", user.UUID).Updates(map[string]any{
		"passwd":   "",
		"sso_type": "github",
		"sso_id":   "sso-user",
	}).Error; err != nil {
		t.Fatal(err)
	}
	remotectl.ResetForTest()
	if err := remotectl.Reauthorize(user.UUID, "", "", "127.0.0.1"); !errors.Is(err, remotectl.ErrPasswordRequired) {
		t.Fatalf("SSO without 2FA still needs a password: %v", err)
	}

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Lite", AccountName: user.Username})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.Enable2Fa(user.UUID, key.Secret(), setup); err != nil {
		t.Fatal(err)
	}
	if err := dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", user.UUID).
		Update("two_factor_counter", 0).Error; err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := remotectl.Reauthorize(user.UUID, "", "", "127.0.0.1"); !errors.Is(err, remotectl.ErrOTPRequired) {
		t.Fatalf("SSO without local password still needs 2FA: %v", err)
	}
	if err := remotectl.Reauthorize(user.UUID, "", code, "127.0.0.1"); err != nil {
		t.Fatalf("SSO with 2FA should authorize remote management: %v", err)
	}
}

func TestReauthorizePasswordAccountStillNeedsPassword(t *testing.T) {
	user, err := accounts.CreateAccount("pw-"+uuid.NewString()[:8], "correctpassword")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = accounts.DeleteAccountByUsername(user.Username) })
	remotectl.ResetForTest()
	if err := remotectl.Reauthorize(user.UUID, "", "", "127.0.0.1"); !errors.Is(err, remotectl.ErrPasswordRequired) {
		t.Fatalf("password account error = %v", err)
	}
	if err := remotectl.Reauthorize(user.UUID, "wrong", "", "127.0.0.1"); !errors.Is(err, remotectl.ErrPasswordInvalid) {
		t.Fatalf("wrong password error = %v", err)
	}
}

var errTOTPRejected = errSentinel("totp rejected")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
