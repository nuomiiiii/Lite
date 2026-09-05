package accounts

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/utils/instancekey"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	cleanup := instancekey.SetupTempFileForTest()
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:accounts_security_test?mode=memory&cache=shared"
	db := dbcore.GetDBInstance()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func TestLegacyPasswordMigratesOnLogin(t *testing.T) {
	username := "lg-" + uuid.NewString()[:8]
	user := models.User{
		UUID:     uuid.NewString(),
		Username: username,
		Passwd:   hashLegacySHA256("LiteTest9a"),
	}
	require.NoError(t, dbcore.GetDBInstance().Create(&user).Error)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	uuid, ok := CheckPassword(username, "LiteTest9a")
	require.True(t, ok)
	require.Equal(t, user.UUID, uuid)
	stored, err := GetUserByUUID(uuid)
	require.NoError(t, err)
	require.True(t, stringsHasArgonPrefix(stored.Passwd))
	require.Contains(t, stored.Passwd, "m=32768,t=3,p=1")
	require.True(t, verifyPasswd("LiteTest9a", stored.Passwd))
}

func stringsHasArgonPrefix(value string) bool {
	return len(value) >= len(argonPrefix) && value[:len(argonPrefix)] == argonPrefix
}

func TestHashedSessionCannotBeUsedAsCookie(t *testing.T) {
	username := "ss-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = DeleteAllSessions()
		_ = DeleteAccountByUsername(username)
	})

	plain, err := CreateSession(user.UUID, 3600, "ua", "127.0.0.1", "password")
	require.NoError(t, err)
	require.NotEmpty(t, plain)

	sessions, err := GetAllSessions()
	require.NoError(t, err)
	var hashed string
	for _, session := range sessions {
		if session.UUID == user.UUID {
			hashed = session.Session
			break
		}
	}
	require.NotEmpty(t, hashed)
	require.NotEqual(t, plain, hashed)

	if _, err := GetSession(hashed); err == nil {
		t.Fatal("database session hash was accepted as a cookie")
	}
	got, err := GetSession(plain)
	require.NoError(t, err)
	require.Equal(t, user.UUID, got)
}

func TestInvalidateAllSessionsDropsRestoredCookies(t *testing.T) {
	username := "rs-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	plain, err := CreateSession(user.UUID, 3600, "ua", "127.0.0.1", "password")
	require.NoError(t, err)
	require.NoError(t, InvalidateAllSessions())
	if _, err := GetSession(plain); err == nil {
		t.Fatal("restored login cookie remained valid")
	}
}

func TestTOTPCounterReplayAcrossLoginAndReauth(t *testing.T) {
	username := "tp-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Lite", AccountName: username})
	require.NoError(t, err)
	encrypted, err := encryptTOTPSecret(key.Secret())
	require.NoError(t, err)
	require.NoError(t, dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", user.UUID).Updates(map[string]interface{}{
		"two_factor":         encrypted,
		"two_factor_counter": 0,
	}).Error)

	now := time.Now()
	code, err := totp.GenerateCodeCustom(key.Secret(), now, totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err)

	ok, err := Verify2Fa(user.UUID, code)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = Verify2Fa(user.UUID, code)
	require.NoError(t, err)
	require.False(t, ok, "serial replay must fail")
}

func TestConcurrentTOTPReplayOnlySucceedsOnce(t *testing.T) {
	username := "tc-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Lite", AccountName: username})
	require.NoError(t, err)
	encrypted, err := encryptTOTPSecret(key.Secret())
	require.NoError(t, err)
	require.NoError(t, dbcore.GetDBInstance().Model(&models.User{}).Where("uuid = ?", user.UUID).Updates(map[string]interface{}{
		"two_factor":         encrypted,
		"two_factor_counter": 0,
	}).Error)

	code, err := totp.GenerateCodeCustom(key.Secret(), time.Now(), totp.ValidateOpts{
		Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	require.NoError(t, err)

	results := make([]bool, 8)
	var wg sync.WaitGroup
	wg.Add(len(results))
	for i := range results {
		go func(i int) {
			defer wg.Done()
			ok, err := Verify2Fa(user.UUID, code)
			results[i] = err == nil && ok
		}(i)
	}
	wg.Wait()
	successes := 0
	for _, ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent TOTP successes = %d, want 1", successes)
	}
}
