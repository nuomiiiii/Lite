package mcp

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMCPTokenTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:mcp-token-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.MCPLease{}, &models.MCPToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	// Mirror production, where the SQLite pool holds a single connection.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
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
func seedApprovedLease(t *testing.T, db *gorm.DB, now time.Time, codeTTL time.Duration) (models.MCPLease, string) {
	t.Helper()
	lease := models.MCPLease{
		ID:                    "ls_" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()),
		OwnerUserUUID:         "user-1",
		OwnerLoginSessionHash: "session-1",
		OAuthClientID:         "cli-test",
		TokenFamilyID:         "tf_" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()),
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
	_, hash, err := newOpaqueToken(tokenPrefixCode)
	if err != nil {
		t.Fatalf("opaque token: %v", err)
	}
	if err := db.Create(&models.MCPToken{
		Hash:      hash,
		Kind:      kindCode,
		FamilyID:  lease.TokenFamilyID,
		LeaseID:   lease.ID,
		ClientID:  lease.OAuthClientID,
		ExpiresAt: now.Add(codeTTL),
		CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create code: %v", err)
	}
	return lease, hash
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
	lease, codeHash := seedApprovedLease(t, db, now, AuthCodeTTL)

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
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	now := time.Now().UTC()
	lease, codeHash := seedApprovedLease(t, db, now, AuthCodeTTL)
	if err := db.Exec(`
		CREATE TRIGGER fail_refresh_insert BEFORE INSERT ON mcp_tokens
		WHEN NEW.kind = 'refresh'
		BEGIN
			SELECT RAISE(ABORT, 'refresh insert failed');
		END
	`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, "https://app.example/cb", "", now); err == nil {
		t.Fatal("redemption must fail when the refresh token cannot be stored")
	}

	var code models.MCPToken
	if err := db.Where("hash = ?", codeHash).First(&code).Error; err != nil {
		t.Fatalf("load code: %v", err)
	}
	if code.Used {
		t.Fatal("code must stay redeemable after a rolled back redemption")
	}
	if got := countTokensOfKind(t, db, kindAccess); got != 0 {
		t.Fatalf("access tokens = %d, want 0 after rollback", got)
	}
	if got := countTokensOfKind(t, db, kindRefresh); got != 0 {
		t.Fatalf("refresh tokens = %d, want 0 after rollback", got)
	}

	if err := db.Exec("DROP TRIGGER fail_refresh_insert").Error; err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := redeemAuthorizationCode(lease, codeHash, lease.OAuthClientID, "https://app.example/cb", "", now); err != nil {
		t.Fatalf("redemption must succeed after the failure is resolved: %v", err)
	}
}

func TestRedeemAuthorizationCodeRejectsRevokedLease(t *testing.T) {
	db := openMCPTokenTestDB(t)
	useMCPTokenTestDB(t, db)
	now := time.Now().UTC()
	lease, codeHash := seedApprovedLease(t, db, now, AuthCodeTTL)
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
			lease, _ := seedApprovedLease(t, db, now, tc.codeTTL)
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
