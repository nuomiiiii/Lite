package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	testRedirectURI  = "https://app.example/cb"
	testPKCEVerifier = "test-verifier-4f3c1a9b8e7d6c5b4a39281706f5e4d3c2b1a09f"
)

func pkceChallengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func openMCPTokenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "mcp.db")) + "?_busy_timeout=5000&_txlock=immediate&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPLease{}, &models.MCPToken{}, &models.MCPOperation{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	// Mirror production, where the SQLite pool holds a single connection.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func useMCPTokenTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	old := database
	database = func() *gorm.DB { return db }
	t.Cleanup(func() { database = old })
}

// seedApprovedLease stores an approval the way approveAuthorization does today:
// an active lease plus its authorization code, before any token was issued.
var mcpTokenTestSeq atomic.Int64

func seedApprovedLease(t *testing.T, db *gorm.DB, now time.Time, codeTTL time.Duration) (models.MCPLease, string, string) {
	t.Helper()
	unique := fmt.Sprintf("%s-%d", strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()), mcpTokenTestSeq.Add(1))
	lease := models.MCPLease{
		ID:                    "ls_" + unique,
		OwnerUserUUID:         "user-1",
		OwnerLoginSessionHash: "session-1",
		OAuthClientID:         "cli-test",
		TokenFamilyID:         "tf_" + unique,
		TargetUUIDs:           "",
		Mode:                  modeFull,
		MaxConcurrency:        4,
		Status:                statusActive,
		PolicyVersion:         PolicyVersion,
		LongTerm:              true,
		CreatedAt:             now,
		ExpiresAt:             now.AddDate(10, 0, 0),
	}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("create lease: %v", err)
	}
	plain, hash, err := newOpaqueToken(tokenPrefixCode)
	if err != nil {
		t.Fatalf("opaque token: %v", err)
	}
	if err := db.Create(&models.MCPToken{
		Hash:        hash,
		Kind:        kindCode,
		FamilyID:    lease.TokenFamilyID,
		LeaseID:     lease.ID,
		ClientID:    lease.OAuthClientID,
		RedirectURI: testRedirectURI,
		ExpiresAt:   now.Add(codeTTL),
		CreatedAt:   now,
	}).Error; err != nil {
		t.Fatalf("create code: %v", err)
	}
	return lease, hash, plain
}

func countTokensOfKind(t *testing.T, db *gorm.DB, kind string) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&models.MCPToken{}).Where("kind = ?", kind).Count(&count).Error; err != nil {
		t.Fatalf("count %s tokens: %v", kind, err)
	}
	return count
}

func TestRedeemAuthorizationCodeIsSingleUse(t *testing.T) {
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	now := time.Now().UTC()
	lease, codeHash, _ := seedApprovedLease(t, db, now, AuthCodeTTL)

	const attempts = 4
	results := make([]error, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, results[index] = redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, "https://app.example/cb", "", now)
		}(i)
	}
	close(start)
	wg.Wait()

	var succeeded int
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errCodeNotRedeemable), errors.Is(err, ErrLeaseInactive):
		default:
			t.Fatalf("unexpected redemption error: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful redemptions = %d, want exactly 1", succeeded)
	}
	if got := countTokensOfKind(t, db, kindAccess); got != 1 {
		t.Fatalf("access tokens = %d, want 1", got)
	}
	if got := countTokensOfKind(t, db, kindRefresh); got != 1 {
		t.Fatalf("refresh tokens = %d, want 1", got)
	}
}

func TestRedeemAuthorizationCodeRollsBackOnTokenInsertFailure(t *testing.T) {
	for _, failingKind := range []string{kindAccess, kindRefresh} {
		t.Run(failingKind+" insert fails", func(t *testing.T) {
			db := openMCPTokenTestDB(t)
			useMCPTokenTestDB(t, db)
			now := time.Now().UTC()
			lease, codeHash, _ := seedApprovedLease(t, db, now, AuthCodeTTL)
			trigger := "fail_" + failingKind + "_insert"
			if err := db.Exec(fmt.Sprintf(`
				CREATE TRIGGER %s BEFORE INSERT ON mcp_tokens
				WHEN NEW.kind = '%s'
				BEGIN
					SELECT RAISE(ABORT, 'token insert failed');
				END
			`, trigger, failingKind)).Error; err != nil {
				t.Fatalf("create trigger: %v", err)
			}

			if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, testRedirectURI, "", now); err == nil {
				t.Fatalf("redemption must fail when the %s token cannot be stored", failingKind)
			}
			var code models.MCPToken
			if err := db.Where("hash = ?", codeHash).First(&code).Error; err != nil {
				t.Fatalf("load code: %v", err)
			}
			if code.Used {
				t.Fatal("code must stay redeemable after a rolled back redemption")
			}
			if got := countTokensOfKind(t, db, kindAccess) + countTokensOfKind(t, db, kindRefresh); got != 0 {
				t.Fatalf("issued tokens = %d, want 0 after rollback", got)
			}

			if err := db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
				t.Fatalf("drop trigger: %v", err)
			}
			if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, testRedirectURI, "", now); err != nil {
				t.Fatalf("redemption must succeed after the failure is resolved: %v", err)
			}
		})
	}
}

// The reported symptom is running out of authorization slots: unredeemed
// approvals accumulate and eventually block a new one.
func TestExpireUnredeemedApprovalsReleasesCapacityForANewApproval(t *testing.T) {
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	now := time.Now().UTC()
	owner := "owner-" + t.Name()
	for i := 0; i < MaxActiveLeasesPerUser; i++ {
		lease, _, _ := seedApprovedLease(t, db, now, -time.Minute)
		if err := db.Model(&models.MCPLease{}).Where("id = ?", lease.ID).Update("owner_user_uuid", owner).Error; err != nil {
			t.Fatalf("assign owner: %v", err)
		}
	}
	live, _, _ := seedApprovedLease(t, db, now, AuthCodeTTL)
	if err := db.Model(&models.MCPLease{}).Where("id = ?", live.ID).Update("owner_user_uuid", owner).Error; err != nil {
		t.Fatalf("assign owner: %v", err)
	}

	if _, user, err := countActiveLeases(owner); err != nil || user != MaxActiveLeasesPerUser+1 {
		t.Fatalf("active leases before cleanup = %d (err %v), want %d", user, err, MaxActiveLeasesPerUser+1)
	}

	if err := expireUnredeemedApprovals(db, now); err != nil {
		t.Fatalf("expire unredeemed approvals: %v", err)
	}

	_, user, err := countActiveLeases(owner)
	if err != nil {
		t.Fatalf("count active leases: %v", err)
	}
	if user != 1 {
		t.Fatalf("active leases after cleanup = %d, want 1 (only the redeemable approval)", user)
	}
	if user >= MaxActiveLeasesPerUser {
		t.Fatalf("capacity still exhausted: %d active leases for a limit of %d", user, MaxActiveLeasesPerUser)
	}
	var stored models.MCPLease
	if err := db.Where("id = ?", live.ID).First(&stored).Error; err != nil {
		t.Fatalf("load redeemable approval: %v", err)
	}
	if stored.Status != statusActive {
		t.Fatalf("redeemable approval status = %q, want %q", stored.Status, statusActive)
	}
}

func TestRedeemAuthorizationCodeRejectsRevokedLease(t *testing.T) {
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	now := time.Now().UTC()
	lease, codeHash, _ := seedApprovedLease(t, db, now, AuthCodeTTL)
	revokedAt := now
	if err := db.Model(&models.MCPLease{}).Where("id = ?", lease.ID).Updates(map[string]any{
		"status":     statusRevoked,
		"revoked_at": revokedAt,
	}).Error; err != nil {
		t.Fatalf("revoke lease: %v", err)
	}

	if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, "https://app.example/cb", "", now); !errors.Is(err, ErrLeaseInactive) {
		t.Fatalf("redeeming a revoked lease returned %v, want ErrLeaseInactive", err)
	}
	var code models.MCPToken
	if err := db.Where("hash = ?", codeHash).First(&code).Error; err != nil {
		t.Fatalf("load code: %v", err)
	}
	if code.Used {
		t.Fatal("a revoked lease must not consume the authorization code")
	}
	if got := countTokensOfKind(t, db, kindAccess) + countTokensOfKind(t, db, kindRefresh); got != 0 {
		t.Fatalf("issued tokens = %d, want 0 for a revoked lease", got)
	}
}

func TestExpireUnredeemedApprovals(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name         string
		codeTTL      time.Duration
		issueTokens  bool
		dropCode     bool
		markUsed     bool
		misbindCode  map[string]any
		addOperation bool
		temporary    bool
		wantStatus   string
		wantReason   string
		wantCapacity int
	}{
		{
			name:         "expired code without tokens stops counting",
			codeTTL:      -time.Minute,
			wantStatus:   statusExpired,
			wantReason:   reasonCodeUnredeemed,
			wantCapacity: 0,
		},
		{
			name:         "redeemable code keeps the approval active",
			codeTTL:      AuthCodeTTL,
			wantStatus:   statusActive,
			wantCapacity: 1,
		},
		{
			name:         "issued credentials keep the approval active",
			codeTTL:      -time.Minute,
			issueTokens:  true,
			wantStatus:   statusActive,
			wantCapacity: 1,
		},
		{
			name:         "consumed but unusable code is retired",
			codeTTL:      -time.Minute,
			markUsed:     true,
			wantStatus:   statusExpired,
			wantReason:   reasonCodeUnredeemed,
			wantCapacity: 0,
		},
		{
			name:         "code bound to another lease is not evidence",
			codeTTL:      -time.Minute,
			misbindCode:  map[string]any{"lease_id": "ls_somewhere_else"},
			wantStatus:   statusActive,
			wantCapacity: 1,
		},
		{
			name:         "code bound to another client is not evidence",
			codeTTL:      -time.Minute,
			misbindCode:  map[string]any{"client_id": "cli_somewhere_else"},
			wantStatus:   statusActive,
			wantCapacity: 1,
		},
		{
			name:         "operation evidence keeps the approval active",
			codeTTL:      -time.Minute,
			addOperation: true,
			wantStatus:   statusActive,
			wantCapacity: 1,
		},
		{
			name:         "temporary approval with an expired code stops counting",
			codeTTL:      -time.Minute,
			temporary:    true,
			wantStatus:   statusExpired,
			wantReason:   reasonCodeUnredeemed,
			wantCapacity: 0,
		},
		{
			name:         "approval without any code row is left alone",
			codeTTL:      -time.Minute,
			dropCode:     true,
			wantStatus:   statusActive,
			wantCapacity: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openMCPTokenTestDB(t)
			useMCPTokenTestDB(t, db)
			lease, _, _ := seedApprovedLease(t, db, now, tc.codeTTL)
			if tc.dropCode {
				if err := db.Where("family_id = ?", lease.TokenFamilyID).Delete(&models.MCPToken{}).Error; err != nil {
					t.Fatalf("drop code: %v", err)
				}
			}
			if tc.markUsed {
				if err := db.Model(&models.MCPToken{}).Where("family_id = ?", lease.TokenFamilyID).Update("used", true).Error; err != nil {
					t.Fatalf("consume code: %v", err)
				}
			}
			if tc.misbindCode != nil {
				if err := db.Model(&models.MCPToken{}).Where("family_id = ?", lease.TokenFamilyID).Updates(tc.misbindCode).Error; err != nil {
					t.Fatalf("misbind code: %v", err)
				}
			}
			if tc.addOperation {
				if err := db.Create(&models.MCPOperation{
					ID: "op_" + lease.ID, LeaseID: lease.ID, AgentUUID: lease.OwnerUserUUID,
					ToolName: "exec", State: "succeeded", CreatedAt: now.Add(-time.Hour),
				}).Error; err != nil {
					t.Fatalf("create operation: %v", err)
				}
			}
			if tc.temporary {
				if err := db.Model(&models.MCPLease{}).Where("id = ?", lease.ID).Updates(map[string]any{
					"long_term": false, "expires_at": now.Add(10 * time.Minute),
				}).Error; err != nil {
					t.Fatalf("shorten approval: %v", err)
				}
				if err := db.Where("id = ?", lease.ID).First(&lease).Error; err != nil {
					t.Fatalf("reload lease: %v", err)
				}
			}
			if tc.issueTokens {
				if err := db.Create(&models.MCPToken{
					Hash: "hash-access", Kind: kindAccess, FamilyID: lease.TokenFamilyID, LeaseID: lease.ID,
					ClientID: lease.OAuthClientID, ExpiresAt: now.Add(time.Minute), CreatedAt: now,
				}).Error; err != nil {
					t.Fatalf("create access token: %v", err)
				}
				if err := db.Create(&models.MCPToken{
					Hash: "hash-refresh", Kind: kindRefresh, FamilyID: lease.TokenFamilyID, LeaseID: lease.ID,
					ClientID: lease.OAuthClientID, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
				}).Error; err != nil {
					t.Fatalf("create refresh token: %v", err)
				}
			}

			if err := expireUnredeemedApprovals(db, now); err != nil {
				t.Fatalf("expire unredeemed approvals: %v", err)
			}

			var stored models.MCPLease
			if err := db.Where("id = ?", lease.ID).First(&stored).Error; err != nil {
				t.Fatalf("load lease: %v", err)
			}
			if stored.Status != tc.wantStatus {
				t.Fatalf("lease status = %q, want %q", stored.Status, tc.wantStatus)
			}
			if stored.RevocationReason != tc.wantReason {
				t.Fatalf("revocation reason = %q, want %q", stored.RevocationReason, tc.wantReason)
			}
			if !stored.ExpiresAt.Equal(lease.ExpiresAt) {
				t.Fatalf("approval expiry moved: %s -> %s", lease.ExpiresAt, stored.ExpiresAt)
			}

			_, user, err := countActiveLeases(lease.OwnerUserUUID)
			if err != nil {
				t.Fatalf("count active leases: %v", err)
			}
			if user != tc.wantCapacity {
				t.Fatalf("active leases for owner = %d, want %d", user, tc.wantCapacity)
			}
		})
	}
}

// The precheck timestamp is taken before the transaction, so the code can expire
// while the request is in flight. Redemption must honour the transaction clock.
func TestRedeemAuthorizationCodeRejectsCodeExpiringAfterPrecheck(t *testing.T) {
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	base := time.Now().UTC()
	lease, codeHash, _ := seedApprovedLease(t, db, base, AuthCodeTTL)
	precheck := base.Add(AuthCodeTTL - time.Minute)
	restore := fixedMCPClock(t, db, base.Add(AuthCodeTTL+time.Second))
	defer restore()

	if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, testRedirectURI, "", precheck); !errors.Is(err, errCodeNotRedeemable) {
		t.Fatalf("redemption returned %v, want errCodeNotRedeemable", err)
	}
	var code models.MCPToken
	if err := db.Where("hash = ?", codeHash).First(&code).Error; err != nil {
		t.Fatalf("load code: %v", err)
	}
	if code.Used {
		t.Fatal("an expired code must not be consumed")
	}
	if got := countTokensOfKind(t, db, kindAccess) + countTokensOfKind(t, db, kindRefresh); got != 0 {
		t.Fatalf("issued tokens = %d, want 0 for an expired code", got)
	}
}

func TestRedeemAuthorizationCodeRejectsMismatchedBinding(t *testing.T) {
	cases := []struct {
		name    string
		updates map[string]any
	}{
		{name: "code belongs to another lease", updates: map[string]any{"lease_id": "ls_other"}},
		{name: "code belongs to another family", updates: map[string]any{"family_id": "tf_other"}},
		{name: "code belongs to another client", updates: map[string]any{"client_id": "cli-other"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openMCPTokenTestDB(t)
			useMCPTokenTestDB(t, db)
			now := time.Now().UTC()
			lease, codeHash, _ := seedApprovedLease(t, db, now, AuthCodeTTL)
			if err := db.Model(&models.MCPToken{}).Where("hash = ?", codeHash).Updates(tc.updates).Error; err != nil {
				t.Fatalf("misbind code: %v", err)
			}

			if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, testRedirectURI, "", now); !errors.Is(err, errCodeNotRedeemable) {
				t.Fatalf("redemption returned %v, want errCodeNotRedeemable", err)
			}
			var code models.MCPToken
			if err := db.Where("hash = ?", codeHash).First(&code).Error; err != nil {
				t.Fatalf("load code: %v", err)
			}
			if code.Used {
				t.Fatal("a misbound code must not be consumed")
			}
			if got := countTokensOfKind(t, db, kindAccess) + countTokensOfKind(t, db, kindRefresh); got != 0 {
				t.Fatalf("issued tokens = %d, want 0 for a misbound code", got)
			}
		})
	}
}

func TestRedeemAuthorizationCodeRejectsLeaseBoundToAnotherClient(t *testing.T) {
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	now := time.Now().UTC()
	lease, codeHash, _ := seedApprovedLease(t, db, now, AuthCodeTTL)
	if err := db.Model(&models.MCPLease{}).Where("id = ?", lease.ID).Update("o_auth_client_id", "cli-other").Error; err != nil {
		t.Fatalf("rebind lease: %v", err)
	}

	if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, testRedirectURI, "", now); !errors.Is(err, errCodeNotRedeemable) {
		t.Fatalf("redemption returned %v, want errCodeNotRedeemable", err)
	}
	if got := countTokensOfKind(t, db, kindAccess) + countTokensOfKind(t, db, kindRefresh); got != 0 {
		t.Fatalf("issued tokens = %d, want 0 when the lease is bound to another client", got)
	}
}

func TestHandleAuthorizationCodeHTTP(t *testing.T) {
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	oldOwner := longTermOwnerLookup
	longTermOwnerLookup = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() { longTermOwnerLookup = oldOwner })
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	lease, _, code := seedApprovedLease(t, db, now, AuthCodeTTL)
	mustSetPKCE(t, db, lease, testPKCEVerifier)

	status, body := postAuthorizationCode(t, code, testPKCEVerifier, lease.OAuthClientID)
	if status != http.StatusOK || body["access_token"] == nil || body["refresh_token"] == nil {
		t.Fatalf("redemption = %d %+v, want 200 with a token pair", status, body)
	}
	if got := countTokensOfKind(t, db, kindAccess); got != 1 {
		t.Fatalf("access tokens = %d, want 1", got)
	}

	status, body = postAuthorizationCode(t, code, testPKCEVerifier, lease.OAuthClientID)
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" || body["access_token"] != nil {
		t.Fatalf("replay = %d %+v, want 400 invalid_grant without credentials", status, body)
	}

	// A storage failure must not be reported as an invalid grant: that would tell
	// the client to re-authorize instead of retrying.
	other, _, otherCode := seedApprovedLease(t, db, now, AuthCodeTTL)
	mustSetPKCE(t, db, other, testPKCEVerifier)
	if err := db.Exec("ALTER TABLE mcp_tokens RENAME TO mcp_tokens_unavailable").Error; err != nil {
		t.Fatalf("detach token table: %v", err)
	}
	status, body = postAuthorizationCode(t, otherCode, testPKCEVerifier, other.OAuthClientID)
	if status != http.StatusInternalServerError || body["error"] != "server_error" {
		t.Fatalf("storage failure = %d %+v, want 500 server_error", status, body)
	}
}

func mustSetPKCE(t *testing.T, db *gorm.DB, lease models.MCPLease, verifier string) {
	t.Helper()
	if err := db.Model(&models.MCPToken{}).
		Where("lease_id = ? AND kind = ?", lease.ID, kindCode).
		Updates(map[string]any{
			"code_challenge":        pkceChallengeFor(verifier),
			"code_challenge_method": pkceS256,
		}).Error; err != nil {
		t.Fatalf("set PKCE: %v", err)
	}
}

func postAuthorizationCode(t *testing.T, code, verifier, clientID string) (int, map[string]any) {
	t.Helper()
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {testRedirectURI},
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "https://lite.invalid/oauth/token", strings.NewReader(values.Encode()))
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAuthorizationCode(ctx, values)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v: %s", err, recorder.Body.String())
	}
	return recorder.Code, body
}

func fixedMCPClock(t *testing.T, db *gorm.DB, at time.Time) func() {
	t.Helper()
	previous := db.Config.NowFunc
	db.Config.NowFunc = func() time.Time { return at }
	return func() { db.Config.NowFunc = previous }
}
