package accounts

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func resetPasswordTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&models.User{}, &models.MCPLease{}, &models.MCPToken{}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []models.User{{UUID: "owner-a", Username: "alice", Passwd: "old-hash"}, {UUID: "owner-b", Username: "bob", Passwd: "other-hash"}} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, id := range []string{"long", "temporary", "other"} {
		owner := "owner-a"
		if id == "other" {
			owner = "owner-b"
		}
		lease := models.MCPLease{ID: id, OwnerUserUUID: owner, OwnerLoginSessionHash: "browser", OAuthClientID: "client", TokenFamilyID: "family-" + id, TargetUUIDs: `["node"]`, Mode: "full", Status: "active", LongTerm: id != "temporary", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		if err := db.Create(&lease).Error; err != nil {
			t.Fatal(err)
		}
		token := models.MCPToken{Hash: id, Kind: "refresh", FamilyID: lease.TokenFamilyID, LeaseID: id, ClientID: "client", CreatedAt: now, ExpiresAt: lease.ExpiresAt}
		if err := db.Create(&token).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestForceResetPasswordPersistsMCPRevocation(t *testing.T) {
	db := resetPasswordTestDB(t)
	owner, err := forceResetPasswordWithDB(db, "alice", "new-hash")
	if err != nil || owner != "owner-a" {
		t.Fatalf("owner=%q err=%v", owner, err)
	}
	var user models.User
	if err := db.First(&user, "uuid = ?", owner).Error; err != nil {
		t.Fatal(err)
	}
	if user.Passwd != "new-hash" {
		t.Fatal("password did not change")
	}
	for _, id := range []string{"long", "temporary", "other"} {
		var lease models.MCPLease
		var token models.MCPToken
		if err := db.First(&lease, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&token, "hash = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if id == "other" {
			if lease.Status != "active" || token.Used {
				t.Fatal("other user's grant affected")
			}
		} else if lease.Status != "revoked" || lease.RevokedAt == nil || lease.RevocationReason != "user_security" || !token.Used || token.ExpiresAt.After(time.Now().UTC()) {
			t.Fatalf("reset did not persist revocation for %s", id)
		}
	}
}

func TestForceResetPasswordRevocationFailureRollsBackPassword(t *testing.T) {
	db := resetPasswordTestDB(t)
	if err := db.Exec("CREATE TRIGGER fail_revoke BEFORE UPDATE ON mcp_leases BEGIN SELECT RAISE(ABORT, 'test revocation failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := forceResetPasswordWithDB(db, "alice", "new-hash"); err == nil {
		t.Fatal("expected transaction failure")
	}
	var user models.User
	if err := db.First(&user, "uuid = ?", "owner-a").Error; err != nil {
		t.Fatal(err)
	}
	if user.Passwd != "old-hash" {
		t.Fatal("password changed without revoking credentials")
	}
	var lease models.MCPLease
	if err := db.First(&lease, "id = ?", "long").Error; err != nil {
		t.Fatal(err)
	}
	if lease.Status != "active" {
		t.Fatal("partial revocation committed")
	}
}
