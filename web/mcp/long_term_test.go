package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/web/remotectl"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These tests deliberately run serially: production uses package-level database,
// owner-validation, feature, and refresh-cache hooks.
func longTermTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.User{}, &models.MCPLease{}, &models.MCPToken{}, &models.MCPClient{}, &models.MCPAuthorizationRequest{}, &models.MCPOperation{}); err != nil {
		t.Fatal(err)
	}
	longTermTestCreate(t, db, &models.User{UUID: "owner", Username: "test-owner", Passwd: "fixture-not-a-real-password"})
	oldDatabase, oldOwner, oldEnabled := database, longTermOwnerExists, isMCPEnabled
	database = func() *gorm.DB { return db }
	longTermOwnerExists = func(uuid string) bool { return uuid == "owner" }
	isMCPEnabled = func() bool { return true }
	refreshMu.Lock()
	oldRefresh := refreshIn
	refreshIn = map[string]*refreshWait{}
	refreshMu.Unlock()
	t.Cleanup(func() {
		database, longTermOwnerExists, isMCPEnabled = oldDatabase, oldOwner, oldEnabled
		refreshMu.Lock()
		refreshIn = oldRefresh
		refreshMu.Unlock()
	})
	t.Setenv(longTermAuthEnv, "false")
	t.Setenv(longTermMigrationEnv, "")
	return db
}

func longTermTestLease(id string, longTerm bool, now time.Time) models.MCPLease {
	return models.MCPLease{
		ID: id, OwnerUserUUID: "owner", OwnerLoginSessionHash: "old-browser-session",
		OAuthClientID: "client", TokenFamilyID: "family-" + id, TargetUUIDs: `["node-a","node-b"]`,
		Mode: modeFull, MaxConcurrency: 2, PolicyVersion: PolicyVersion,
		Status: statusActive, LongTerm: longTerm, CreatedAt: now, ExpiresAt: longTermExpiresAt(),
	}
}

func longTermTestCreate(t *testing.T, db *gorm.DB, row any) {
	t.Helper()
	if err := db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
}

func longTermTestStoredLease(t *testing.T, db *gorm.DB, id string) models.MCPLease {
	t.Helper()
	var lease models.MCPLease
	if err := db.Where("id = ?", id).First(&lease).Error; err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestLongTermAuthorizationPolicyOptIn(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	yes, no := true, false
	cases := []struct {
		name, env               string
		requested               *bool
		wantLongTerm, wantError bool
	}{
		{"unset-default-temporary", "", nil, false, false},
		{"false-default-temporary", "false", nil, false, false},
		{"invalid-env-default-temporary", "not-a-bool", nil, false, false},
		{"enabled-omitted-remains-temporary", "true", nil, false, false},
		{"enabled-explicit-long-term", " true ", &yes, true, false},
		{"enabled-explicit-temporary", "true", &no, false, false},
		{"disabled-explicit-temporary", "false", &no, false, false},
		{"disabled-rejects-long-term", "false", &yes, false, true},
		{"unset-rejects-long-term", "", &yes, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(longTermAuthEnv, tc.env)
			longTerm, expires, err := authorizationPolicy(tc.requested, now, 45)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tc.wantError)
			}
			if tc.wantError {
				return
			}
			wantExpiry := now.Add(45 * time.Minute)
			if tc.wantLongTerm {
				wantExpiry = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			if longTerm != tc.wantLongTerm || !expires.Equal(wantExpiry) {
				t.Fatalf("policy = (%v, %v), want (%v, %v)", longTerm, expires, tc.wantLongTerm, wantExpiry)
			}
		})
	}
}

func TestLongTermMigrationOnlyNamedLiveLeasesPreservesGrant(t *testing.T) {
	db := longTermTestDB(t)
	t.Setenv(longTermAuthEnv, "true")
	now := time.Now().UTC()
	live := longTermTestLease("named-live", false, now)
	live.ExpiresAt = now.Add(2 * time.Hour)
	unnamed := longTermTestLease("unnamed-live", false, now)
	revoked := longTermTestLease("named-revoked", false, now)
	revoked.Status, revoked.RevokedAt, revoked.RevocationReason = statusRevoked, &now, reasonRevoked
	expired := longTermTestLease("named-expired", false, now)
	expired.ExpiresAt = now.Add(-time.Minute)
	for _, row := range []*models.MCPLease{&live, &unnamed, &revoked, &expired} {
		longTermTestCreate(t, db, row)
	}
	t.Setenv(longTermMigrationEnv, " named-live, named-revoked,named-expired,named-live ")
	for i := 0; i < 2; i++ {
		if err := promoteConfiguredLongTermLeases(); err != nil {
			t.Fatal(err)
		}
	}
	got := longTermTestStoredLease(t, db, live.ID)
	got.LongTerm = false
	if !reflect.DeepEqual(got, live) {
		t.Fatalf("migration changed grant fields beyond LongTerm:\ngot  %+v\nwant %+v", got, live)
	}
	if !longTermTestStoredLease(t, db, live.ID).LongTerm {
		t.Fatal("explicit live lease was not promoted")
	}
	for _, original := range []models.MCPLease{unnamed, revoked, expired} {
		if got := longTermTestStoredLease(t, db, original.ID); !reflect.DeepEqual(got, original) {
			t.Fatalf("migration altered unselected/inactive lease %s: %+v", original.ID, got)
		}
	}
	// A migration list left in the environment cannot undo a later revocation.
	if err := revokeFamily(live.TokenFamilyID, reasonRevoked); err != nil {
		t.Fatal(err)
	}
	if err := promoteConfiguredLongTermLeases(); err != nil {
		t.Fatal(err)
	}
	if got := longTermTestStoredLease(t, db, live.ID); got.Status != statusRevoked || got.RevokedAt == nil {
		t.Fatalf("migration resurrected revoked promoted lease: %+v", got)
	}
}

func TestLongTermMigrationDisabledDoesNotPromote(t *testing.T) {
	db := longTermTestDB(t)
	lease := longTermTestLease("named", false, time.Now().UTC())
	longTermTestCreate(t, db, &lease)
	t.Setenv(longTermMigrationEnv, lease.ID)
	if err := promoteConfiguredLongTermLeases(); err != nil {
		t.Fatal(err)
	}
	if longTermTestStoredLease(t, db, lease.ID).LongTerm {
		t.Fatal("disabled feature promoted a lease")
	}
}

func TestLongTermRestartAndLoginRevocationSkipLongTerm(t *testing.T) {
	for _, reason := range []string{reasonRestart, reasonLoginRevoked} {
		t.Run(reason, func(t *testing.T) {
			db := longTermTestDB(t)
			now := time.Now().UTC()
			long := longTermTestLease("long", true, now)
			temporary := longTermTestLease("temporary", false, now)
			for _, lease := range []*models.MCPLease{&long, &temporary} {
				longTermTestCreate(t, db, lease)
				longTermTestCreate(t, db, &models.MCPToken{Hash: lease.ID, Kind: kindRefresh, FamilyID: lease.TokenFamilyID, LeaseID: lease.ID, ClientID: "client", CreatedAt: now, ExpiresAt: lease.ExpiresAt})
			}
			var err error
			if reason == reasonRestart {
				err = InvalidateActiveLeases()
			} else {
				err = revokeActiveLeases("", temporary.OwnerLoginSessionHash, reason)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, lease := range []models.MCPLease{long, temporary} {
				got := longTermTestStoredLease(t, db, lease.ID)
				var token models.MCPToken
				if err := db.Where("hash = ?", lease.ID).First(&token).Error; err != nil {
					t.Fatal(err)
				}
				if lease.LongTerm {
					if got.Status != statusActive || got.RevokedAt != nil || token.Used || !token.ExpiresAt.Equal(lease.ExpiresAt) {
						t.Fatalf("%s revoked long-term authorization/token", reason)
					}
				} else if got.Status != statusRevoked || got.RevokedAt == nil || got.RevocationReason != reason || !token.Used || token.ExpiresAt.After(time.Now().UTC()) {
					t.Fatalf("%s did not revoke temporary authorization/token: %+v %+v", reason, got, token)
				}
			}
		})
	}
}

func TestLongTermSecurityExplicitAndDisableRevocation(t *testing.T) {
	for _, reason := range []string{reasonUserSecurity, reasonRevoked, reasonMCPOff, reasonRemoteOff} {
		t.Run(reason, func(t *testing.T) {
			db := longTermTestDB(t)
			now := time.Now().UTC()
			lease := longTermTestLease("long", true, now)
			longTermTestCreate(t, db, &lease)
			longTermTestCreate(t, db, &models.MCPToken{Hash: "refresh", Kind: kindRefresh, FamilyID: lease.TokenFamilyID, LeaseID: lease.ID, ClientID: "client", CreatedAt: now, ExpiresAt: lease.ExpiresAt})
			switch reason {
			case reasonUserSecurity:
				RevokeUser(lease.OwnerUserUUID)
			case reasonMCPOff:
				// This is also the explicit admin revoke-all path.
				DrainMCPDelivery()
			case reasonRemoteOff:
				// Exercise the actual remote-disable listener registered by init.
				remotectl.RevokeAll()
			default:
				if err := RevokeAllActive(""); err != nil {
					t.Fatal(err)
				}
			}
			got := longTermTestStoredLease(t, db, lease.ID)
			if got.Status != statusRevoked || got.RevokedAt == nil || got.RevocationReason != reason {
				t.Fatalf("long-term authorization survived %s: %+v", reason, got)
			}
			var token models.MCPToken
			if err := db.First(&token).Error; err != nil {
				t.Fatal(err)
			}
			if !token.Used || token.ExpiresAt.After(time.Now().UTC()) {
				t.Fatalf("long-term token survived %s: %+v", reason, token)
			}
		})
	}
}

func TestLongTermLoadLiveLeaseIgnoresBrowserSessionButRequiresOwner(t *testing.T) {
	for _, browser := range []string{"", "expired-browser-session"} {
		t.Run("browser="+browser, func(t *testing.T) {
			db := longTermTestDB(t)
			now := time.Now().UTC()
			lease := longTermTestLease("long", true, now)
			lease.OwnerLoginSessionHash = browser
			longTermTestCreate(t, db, &lease)
			longTermTestCreate(t, db, &models.MCPToken{Hash: "refresh", Kind: kindRefresh, FamilyID: lease.TokenFamilyID, LeaseID: lease.ID, ClientID: "client", CreatedAt: now, ExpiresAt: lease.ExpiresAt})
			if got, err := loadLiveLease(lease.ID, now); err != nil || got.ID != lease.ID {
				t.Fatalf("long-term lease required browser session: %+v %v", got, err)
			}
			longTermOwnerExists = func(string) bool { return false }
			if _, err := loadLiveLease(lease.ID, now); !errors.Is(err, ErrLeaseInactive) {
				t.Fatalf("deleted owner error = %v, want ErrLeaseInactive", err)
			}
			if got := longTermTestStoredLease(t, db, lease.ID); got.Status != statusRevoked || got.RevocationReason != reasonUserSecurity {
				t.Fatalf("deleted owner did not revoke lease: %+v", got)
			}
			var token models.MCPToken
			if err := db.First(&token).Error; err != nil {
				t.Fatal(err)
			}
			if !token.Used {
				t.Fatal("deleted owner did not revoke refresh token")
			}
		})
	}
}

func TestLongTermCleanupKeepsValidOldRefreshAndLeaseBoundClient(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Now().UTC()
	old := now.Add(-4 * 24 * time.Hour)
	lease := longTermTestLease("long", true, old)
	lease.OAuthClientID = "lease-bound"
	longTermTestCreate(t, db, &lease)
	for _, id := range []string{"lease-bound", "token-bound", "unused"} {
		longTermTestCreate(t, db, &models.MCPClient{ClientID: id, CreatedAt: old})
	}
	for _, token := range []models.MCPToken{
		{Hash: "valid-old", Kind: kindRefresh, ClientID: "token-bound", ExpiresAt: longTermExpiresAt()},
		{Hash: "used-old", Kind: kindRefresh, ClientID: "token-bound", Used: true, ExpiresAt: longTermExpiresAt()},
		{Hash: "expired-old", Kind: kindRefresh, ClientID: "token-bound", ExpiresAt: now.Add(-time.Minute)},
	} {
		token.LeaseID, token.FamilyID, token.CreatedAt = lease.ID, lease.TokenFamilyID, old
		longTermTestCreate(t, db, &token)
	}
	if !db.Migrator().HasColumn(&models.MCPLease{}, "o_auth_client_id") {
		t.Fatal("fixture must use GORM's actual o_auth_client_id column")
	}
	if err := cleanupHistory(db, now); err != nil {
		t.Fatal(err)
	}
	var tokens []models.MCPToken
	if err := db.Find(&tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Hash != "valid-old" || !tokens[0].ExpiresAt.Equal(longTermExpiresAt()) {
		t.Fatalf("cleanup tokens = %+v, want only still-valid old refresh", tokens)
	}
	var clients []models.MCPClient
	if err := db.Order("client_id").Find(&clients).Error; err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 || clients[0].ClientID != "lease-bound" || clients[1].ClientID != "token-bound" {
		t.Fatalf("cleanup clients = %+v, want lease-bound + token-bound", clients)
	}
	if count := unusedMCPClientCount(); count != 0 {
		t.Fatalf("unused clients = %d, want 0", count)
	}
	if got := longTermTestStoredLease(t, db, lease.ID); !leaseLive(got, now) {
		t.Fatal("cleanup removed/invalidated live old long-term lease")
	}
}

func TestLongTermIssueTokenPairUsesShortAccessAnd2100Refresh(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	lease := longTermTestLease("long", true, now)
	pair, err := issueTokenPair(lease, "client", "https://client.invalid/callback", "https://lite.invalid/mcp", now)
	if err != nil {
		t.Fatal(err)
	}
	if pair.ExpiresIn != 300 || pair.Scope != scopeAgentFull || pair.TokenType != "Bearer" {
		t.Fatalf("unexpected token response: %+v", pair)
	}
	for _, item := range []struct {
		plain, kind string
		expiry      time.Time
	}{
		{pair.Access, kindAccess, now.Add(5 * time.Minute)},
		{pair.Refresh, kindRefresh, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		var token models.MCPToken
		if err := db.Where("hash = ?", hashToken(item.plain)).First(&token).Error; err != nil {
			t.Fatal(err)
		}
		if token.Kind != item.kind || !token.ExpiresAt.Equal(item.expiry) || token.ClientID != "client" || token.LeaseID != lease.ID || token.FamilyID != lease.TokenFamilyID {
			t.Fatalf("stored %s token = %+v, want expiry %v and grant binding", item.kind, token, item.expiry)
		}
	}
}

func longTermTestRefreshFixture(t *testing.T) (*gorm.DB, models.MCPLease, tokenPair) {
	t.Helper()
	db := longTermTestDB(t)
	now := time.Now().UTC()
	lease := longTermTestLease("long", true, now)
	lease.OwnerLoginSessionHash = ""
	longTermTestCreate(t, db, &lease)
	pair, err := issueTokenPair(lease, "client", "https://client.invalid/callback", "https://lite.invalid/mcp", now)
	if err != nil {
		t.Fatal(err)
	}
	return db, lease, pair
}

func longTermTestRefreshHTTP(t *testing.T, refresh, client string) (int, map[string]any) {
	t.Helper()
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {client}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "https://lite.invalid/oauth/token", strings.NewReader(values.Encode()))
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleToken(ctx)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v: %s", err, recorder.Body.String())
	}
	return recorder.Code, body
}

func TestLongTermRefreshRetrySamePairAndClientBindingBeforeCache(t *testing.T) {
	db, lease, original := longTermTestRefreshFixture(t)
	status, first := longTermTestRefreshHTTP(t, original.Refresh, "client")
	if status != http.StatusOK || first["access_token"] == original.Access || first["refresh_token"] == original.Refresh {
		t.Fatalf("initial rotation = %d %+v", status, first)
	}
	status, mismatch := longTermTestRefreshHTTP(t, original.Refresh, "other-client")
	if status != http.StatusBadRequest || mismatch["error"] != "invalid_client" || mismatch["access_token"] != nil {
		t.Fatalf("cached response leaked to mismatching client: %d %+v", status, mismatch)
	}
	status, retry := longTermTestRefreshHTTP(t, original.Refresh, "client")
	if status != http.StatusOK || !reflect.DeepEqual(first, retry) {
		t.Fatalf("immediate retry = %d %+v, want identical pair %+v", status, retry, first)
	}
	if got := longTermTestStoredLease(t, db, lease.ID); !leaseLive(got, time.Now().UTC()) {
		t.Fatalf("legitimate retry revoked lease: %+v", got)
	}
	var count int64
	if err := db.Model(&models.MCPToken{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("retry minted extra tokens: count = %d, want 4", count)
	}
}

func TestLongTermRefreshReplayOutsideGraceRevokesFamily(t *testing.T) {
	db, lease, original := longTermTestRefreshFixture(t)
	if status, body := longTermTestRefreshHTTP(t, original.Refresh, "client"); status != http.StatusOK {
		t.Fatalf("initial refresh = %d %+v", status, body)
	}
	// Expire the retry entry deterministically, without a 30-second sleep.
	refreshMu.Lock()
	delete(refreshIn, original.Refresh)
	refreshMu.Unlock()
	status, body := longTermTestRefreshHTTP(t, original.Refresh, "client")
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("replay = %d %+v", status, body)
	}
	if got := longTermTestStoredLease(t, db, lease.ID); got.Status != statusRevoked || got.RevocationReason != reasonRefreshReuse {
		t.Fatalf("replay did not revoke family: %+v", got)
	}
	var live int64
	if err := db.Model(&models.MCPToken{}).Where("family_id = ? AND (used = ? OR expires_at > ?)", lease.TokenFamilyID, false, time.Now().UTC()).Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("replayed family has %d usable/unexpired tokens", live)
	}
}

func TestLongTermAdminRevocationInvalidatesRefreshCache(t *testing.T) {
	db, lease, original := longTermTestRefreshFixture(t)
	status, rotated := longTermTestRefreshHTTP(t, original.Refresh, "client")
	if status != http.StatusOK {
		t.Fatalf("initial refresh = %d %+v", status, rotated)
	}
	// revokeFamily is the exact revocation path used by the admin revoke handler.
	if err := revokeFamily(lease.TokenFamilyID, reasonRevoked); err != nil {
		t.Fatal(err)
	}
	for _, refresh := range []string{original.Refresh, rotated["refresh_token"].(string)} {
		status, body := longTermTestRefreshHTTP(t, refresh, "client")
		if status != http.StatusBadRequest || body["error"] != "invalid_grant" || body["access_token"] != nil {
			t.Fatalf("admin-revoked cached/successor refresh = %d %+v", status, body)
		}
	}
	if got := longTermTestStoredLease(t, db, lease.ID); got.Status != statusRevoked {
		t.Fatal("admin-revoked lease became active")
	}
}

func TestLongTermRefreshSuccessorInsertFailureRollsBack(t *testing.T) {
	db, lease, original := longTermTestRefreshFixture(t)
	// Fail the second successor insert, after both consuming the predecessor
	// and inserting the new access token, to exercise transaction rollback.
	if err := db.Exec(`CREATE TRIGGER fail_successor_refresh BEFORE INSERT ON mcp_tokens WHEN NEW.kind = 'refresh' BEGIN SELECT RAISE(ABORT, 'injected successor failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	status, body := longTermTestRefreshHTTP(t, original.Refresh, "client")
	if status != http.StatusInternalServerError || body["error"] != "server_error" {
		t.Fatalf("failed rotation = %d %+v", status, body)
	}
	var predecessor models.MCPToken
	if err := db.Where("hash = ?", hashToken(original.Refresh)).First(&predecessor).Error; err != nil {
		t.Fatal(err)
	}
	if predecessor.Used || !predecessor.ExpiresAt.Equal(lease.ExpiresAt) {
		t.Fatalf("rollback consumed/expired predecessor: %+v", predecessor)
	}
	var count int64
	if err := db.Model(&models.MCPToken{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("rollback leaked successor access token: count = %d, want 2", count)
	}
	if got := longTermTestStoredLease(t, db, lease.ID); !leaseLive(got, time.Now().UTC()) {
		t.Fatalf("failed rotation revoked lease: %+v", got)
	}
	if err := db.Exec("DROP TRIGGER fail_successor_refresh").Error; err != nil {
		t.Fatal(err)
	}
	if status, body := longTermTestRefreshHTTP(t, original.Refresh, "client"); status != http.StatusOK {
		t.Fatalf("retry after rollback = %d %+v", status, body)
	}
}
