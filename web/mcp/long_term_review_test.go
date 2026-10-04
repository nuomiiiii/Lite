package mcp

import (
	"net/http"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

func TestLongTermMigrationUsesSingleTransactionConnection(t *testing.T) {
	db := longTermTestDB(t) // MaxOpenConns(1), real users table.
	t.Setenv(longTermAuthEnv, "true")
	lease := longTermTestLease("single-connection", false, time.Now().UTC())
	longTermTestCreate(t, db, &lease)
	t.Setenv(longTermMigrationEnv, lease.ID)
	longTermOwnerExists = func(string) bool {
		t.Fatal("migration must validate the owner through its transaction, not another connection")
		return false
	}
	if err := promoteConfiguredLongTermLeases(); err != nil {
		t.Fatal(err)
	}
	if !longTermTestStoredLease(t, db, lease.ID).LongTerm {
		t.Fatal("lease not promoted")
	}
}

func TestLongTermMigrationMissingOwnerRollsBack(t *testing.T) {
	db := longTermTestDB(t)
	t.Setenv(longTermAuthEnv, "true")
	lease := longTermTestLease("missing-owner", false, time.Now().UTC())
	lease.OwnerUserUUID = "deleted-owner"
	longTermTestCreate(t, db, &lease)
	t.Setenv(longTermMigrationEnv, lease.ID)
	if err := promoteConfiguredLongTermLeases(); err == nil {
		t.Fatal("missing owner accepted")
	}
	if longTermTestStoredLease(t, db, lease.ID).LongTerm {
		t.Fatal("invalid grant was promoted")
	}
}

func TestLongTermAdministrationKeepsOldLiveGrantsVisible(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Now().UTC()
	live := longTermTestLease("old-live", true, now.Add(-10*24*time.Hour))
	expired := longTermTestLease("old-expired", true, live.CreatedAt)
	expired.Status, expired.ExpiresAt = statusExpired, now.Add(-time.Hour)
	recent := longTermTestLease("recent-revoked", false, now.Add(-time.Hour))
	recent.Status, recent.RevokedAt = statusRevoked, &now
	for _, row := range []*models.MCPLease{&live, &expired, &recent} {
		longTermTestCreate(t, db, row)
	}
	rows, err := visibleAdminLeases(db, now)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if len(rows) != 2 || !seen[live.ID] || !seen[recent.ID] || seen[expired.ID] {
		t.Fatalf("unexpected visible grants: %v", seen)
	}
}

func TestLongTermRefreshConsumptionRaceRevokesFamily(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Now().UTC()
	lease := longTermTestLease("refresh-race", true, now)
	longTermTestCreate(t, db, &lease)
	pair, err := issueTokenPair(lease, "client", "https://app.example/cb", "https://lite.example/mcp", now)
	if err != nil {
		t.Fatal(err)
	}
	consumed := false
	callback := "test:consume-predecessor"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "mcp_tokens" && !consumed {
			consumed = true
			if err := tx.Exec("UPDATE mcp_tokens SET used = 1 WHERE hash = ?", hashToken(pair.Refresh)).Error; err != nil {
				t.Fatal(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	response, _ := longTermTestRefreshHTTP(t, pair.Refresh, "client")
	if response != http.StatusBadRequest {
		t.Fatalf("status=%d, want invalid_grant", response)
	}
	if got := longTermTestStoredLease(t, db, lease.ID); got.Status != statusRevoked || got.RevocationReason != reasonRefreshReuse {
		t.Fatalf("consumption race did not revoke: %+v", got)
	}
}
