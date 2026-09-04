package accounts

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"
)

func TestExistingArgon2idKeepsRecordedParams(t *testing.T) {
	username := "ag-" + uuid.NewString()[:8]
	encoded := encodeArgon2id("LiteTest9a", 2, 64*1024, 1, 32)
	require.Contains(t, encoded, "m=65536,t=2,p=1")
	user := models.User{
		UUID:     uuid.NewString(),
		Username: username,
		Passwd:   encoded,
	}
	require.NoError(t, dbcore.GetDBInstance().Create(&user).Error)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	got, err := AuthenticatePassword(username, "LiteTest9a", "")
	require.NoError(t, err)
	require.Equal(t, user.UUID, got)
	stored, err := GetUserByUUID(user.UUID)
	require.NoError(t, err)
	require.Equal(t, encoded, stored.Passwd)
}

func TestParseArgon2idRejectsOversizedAndMalformed(t *testing.T) {
	validSalt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	validHash := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	tests := []string{
		"$argon2id$v=19$m=1048576,t=2,p=1$" + validSalt + "$" + validHash,
		"$argon2id$v=19$m=0,t=3,p=1$" + validSalt + "$" + validHash,
		"$argon2id$v=19$m=32768,t=0,p=1$" + validSalt + "$" + validHash,
		"$argon2id$v=19$m=32768,t=3,p=0$" + validSalt + "$" + validHash,
		"$argon2id$v=19$m=32768,t=9,p=1$" + validSalt + "$" + validHash,
		"$argon2id$v=19$m=32768,t=3,p=8$" + validSalt + "$" + validHash,
		"$argon2id$v=19$m=32768,t=3,p=1$not-base64$" + validHash,
		"not-argon",
	}
	for _, encoded := range tests {
		if _, _, _, _, _, _, ok := parseArgon2id(encoded); ok {
			t.Fatalf("accepted oversized or malformed hash %q", encoded)
		}
		if verifyArgon2id("LiteTest9a", encoded) {
			t.Fatalf("verified oversized or malformed hash %q", encoded)
		}
	}
}

func TestArgon2VerifyConcurrencyRejectsWithoutQueue(t *testing.T) {
	username := "busy-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	release, ok := HoldArgon2ForTest()
	require.True(t, ok)
	defer release()

	start := time.Now()
	_, err = AuthenticatePassword(username, "LiteTest9a", "")
	require.ErrorIs(t, err, ErrPasswordBusy)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("busy password check queued instead of returning immediately: %s", time.Since(start))
	}
	require.ErrorIs(t, VerifyPasswordForUUID(user.UUID, "LiteTest9a"), ErrPasswordBusy)
}

func TestLoginLimitAppliesBeforeArgon2AndHasCapacityAndExpiry(t *testing.T) {
	ResetLoginLimitsForTest()
	t.Cleanup(ResetLoginLimitsForTest)

	username := "lim-" + uuid.NewString()[:8]
	_, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	ip := "203.0.113.10"
	for i := 0; i < loginLimitMaxFailures; i++ {
		_, err := AuthenticatePassword(username, "wrong-password", ip)
		require.ErrorIs(t, err, ErrPasswordInvalid)
	}
	started := 0
	SetArgon2VerifyObserverForTest(func() { started++ })
	t.Cleanup(func() { SetArgon2VerifyObserverForTest(nil) })
	_, err = AuthenticatePassword(username, "LiteTest9a", ip)
	require.ErrorIs(t, err, ErrPasswordBusy)
	require.Equal(t, 0, started)

	ResetLoginLimitsForTest()
	for i := 0; i < loginLimitCapacity+32; i++ {
		recordLoginFailure(fmt.Sprintf("198.51.100.%d", i%256)+fmt.Sprintf(":%d", i), "user-"+fmt.Sprintf("%d", i))
	}
	if got := LoginLimitSizeForTest(); got > loginLimitCapacity {
		t.Fatalf("login limit size = %d, want <= %d", got, loginLimitCapacity)
	}

	ResetLoginLimitsForTest()
	for i := 0; i < loginLimitMaxFailures; i++ {
		recordLoginFailure(ip, username)
	}
	require.True(t, loginThrottled(ip, username))
	ExpireLoginLimitsForTest(loginLimitWindow + time.Second)
	require.False(t, loginThrottled(ip, username))
}

func TestLoginLimitDoesNotScanWholeTableOnEveryRequest(t *testing.T) {
	ResetLoginLimitsForTest()
	t.Cleanup(ResetLoginLimitsForTest)
	for i := 0; i < 10; i++ {
		recordLoginFailure(fmt.Sprintf("10.0.0.%d", i), "user")
	}
	ExpireLoginLimitsForTest(loginLimitWindow + time.Second)
	if got := LoginLimitSizeForTest(); got != 10 {
		t.Fatalf("expired entries = %d, want 10", got)
	}
	require.False(t, loginThrottled("203.0.113.9", "other"))
	if got := LoginLimitSizeForTest(); got != 10 {
		t.Fatalf("single check pruned the whole table: %d", got)
	}
	for i := 0; i < loginLimitPruneEveryOps; i++ {
		_ = loginThrottled("203.0.113.8", "periodic")
	}
	if got := LoginLimitSizeForTest(); got != 0 {
		t.Fatalf("periodic prune size = %d, want 0", got)
	}
}

func TestArgon2GenerateAndVerifyShareSingleSlot(t *testing.T) {
	username := "slot-" + uuid.NewString()[:8]
	_, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	started := make(chan struct{})
	done := make(chan struct{})
	finished := make(chan struct{})
	SetArgon2VerifyObserverForTest(func() {
		select {
		case <-started:
		default:
			close(started)
		}
		<-done
	})
	t.Cleanup(func() { SetArgon2VerifyObserverForTest(nil) })

	go func() {
		defer close(finished)
		_, _ = hashPasswd("LiteTest9a")
	}()
	<-started
	start := time.Now()
	_, err = hashPasswd("LiteTest9a")
	require.ErrorIs(t, err, ErrPasswordBusy)
	_, err = AuthenticatePassword(username, "LiteTest9a", "")
	require.ErrorIs(t, err, ErrPasswordBusy)
	_, err = CreateAccount("busy-create-"+uuid.NewString()[:8], "LiteTest9a")
	require.ErrorIs(t, err, ErrPasswordBusy)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("argon2 generate/verify queued instead of returning immediately: %s", time.Since(start))
	}
	close(done)
	<-finished
}

func TestLegacyPasswordUpgradeSkippedWhenArgon2Busy(t *testing.T) {
	username := "lgb-" + uuid.NewString()[:8]
	user := models.User{
		UUID:     uuid.NewString(),
		Username: username,
		Passwd:   hashLegacySHA256("LiteTest9a"),
	}
	require.NoError(t, dbcore.GetDBInstance().Create(&user).Error)
	t.Cleanup(func() { _ = DeleteAccountByUsername(username) })

	release, ok := HoldArgon2ForTest()
	require.True(t, ok)
	defer release()

	got, err := AuthenticatePassword(username, "LiteTest9a", "")
	require.NoError(t, err)
	require.Equal(t, user.UUID, got)
	stored, err := GetUserByUUID(user.UUID)
	require.NoError(t, err)
	require.Equal(t, user.Passwd, stored.Passwd)
	require.True(t, isLegacyPasswordHash(stored.Passwd))
}

func encodeArgon2id(passwd string, timeCost, memory uint32, threads uint8, keyLen uint32) string {
	salt := []byte("0123456789abcdef")
	sum := argon2.IDKey([]byte(passwd), salt, timeCost, memory, threads, keyLen)
	return fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s",
		argonPrefix,
		argon2.Version,
		memory,
		timeCost,
		threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	)
}

func TestSessionStillValidUsesOneQuery(t *testing.T) {
	username := "sv-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = DeleteAllSessions()
		_ = DeleteAccountByUsername(username)
	})
	plain, err := CreateSession(user.UUID, 3600, "ua", "127.0.0.1", "password")
	require.NoError(t, err)

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

	db := dbcore.GetDBInstance()
	if !SessionStillValid(user.UUID, plain) {
		t.Fatal("plaintext login session should still be valid")
	}
	if !SessionStillValid(user.UUID, hashed) {
		t.Fatal("hashed login session should still be valid")
	}
	var found int
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Model(&models.Session{}).
			Select("1").
			Joins("INNER JOIN users ON users.uuid = sessions.uuid").
			Where("sessions.session = ? AND sessions.uuid = ? AND sessions.expires > ?", hashed, user.UUID, time.Now().UTC()).
			Limit(1).
			Scan(&found)
	})
	lower := strings.ToLower(sql)
	if strings.Count(lower, "select") != 1 || !strings.Contains(lower, "sessions") || !strings.Contains(lower, "join users") {
		t.Fatalf("session validity SQL = %q", sql)
	}
	if SessionStillValid("other-user", hashed) {
		t.Fatal("session belonging to another user was accepted")
	}
}

func TestSessionStillValidRejectsExpiredAndMissingUser(t *testing.T) {
	username := "sx-" + uuid.NewString()[:8]
	user, err := CreateAccount(username, "LiteTest9a")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = DeleteAllSessions()
		_ = DeleteAccountByUsername(username)
	})
	plain, err := CreateSession(user.UUID, 3600, "ua", "127.0.0.1", "password")
	require.NoError(t, err)
	require.True(t, SessionStillValid(user.UUID, plain))

	require.NoError(t, dbcore.GetDBInstance().Model(&models.Session{}).Where("uuid = ?", user.UUID).Update("expires", time.Now().UTC().Add(-time.Minute)).Error)
	if SessionStillValid(user.UUID, plain) {
		t.Fatal("expired session was accepted")
	}

	plain2, err := CreateSession(user.UUID, 3600, "ua", "127.0.0.1", "password")
	require.NoError(t, err)
	require.NoError(t, DeleteAccountByUsername(username))
	if SessionStillValid(user.UUID, plain2) {
		t.Fatal("session without administrator user was accepted")
	}
}
