package mcp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

// Long-term authorization is opt-in at the instance and per-lease levels.
// Access tokens remain short lived; account-security and explicit revocation
// continue to invalidate these leases. This is not an administrator API key.
const longTermAuthEnv = "LITE_MCP_LONG_TERM_AUTH"
const longTermMigrationEnv = "LITE_MCP_LONG_TERM_LEASE_IDS"

var longTermOwnerExists = func(uuid string) bool {
	_, err := accounts.GetUserByUUID(uuid)
	return uuid != "" && err == nil
}

func longTermAuthorizationEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(longTermAuthEnv)))
	return err == nil && enabled
}

func longTermExpiresAt() time.Time {
	return time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func authorizationPolicy(requested *bool, now time.Time, minutes int) (bool, time.Time, error) {
	longTerm := false // Omitted/null requests retain the temporary policy.
	if requested != nil {
		longTerm = *requested
	}
	if longTerm && !longTermAuthorizationEnabled() {
		return false, time.Time{}, fmt.Errorf("long-term MCP authorization is not enabled")
	}
	if longTerm {
		expires := longTermExpiresAt()
		if !expires.After(now) {
			return false, time.Time{}, ErrLeaseInactive
		}
		return true, expires, nil
	}
	return false, leaseExpiresAt(now, minutes), nil
}

// Migrate only explicitly named, already-live leases. Never restore an expired
// or revoked grant, alter its node scope, or extend its existing expiration.
func promoteConfiguredLongTermLeases() error {
	if !longTermAuthorizationEnabled() {
		return nil
	}
	ids := compactStrings(strings.Split(os.Getenv(longTermMigrationEnv), ","))
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return database().Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			var lease models.MCPLease
			if err := tx.Where("id = ?", id).First(&lease).Error; err != nil {
				return fmt.Errorf("long-term migration lease %s: %w", id, err)
			}
			// A once-off migration list may remain configured after revocation.
			// Skip inactive rows rather than resurrecting them on the next restart.
			if lease.LongTerm || !leaseLive(lease, now) {
				continue
			}
			var ownerCount int64
			if err := tx.Model(&models.User{}).Where("uuid = ?", lease.OwnerUserUUID).Count(&ownerCount).Error; err != nil {
				return err
			}
			if ownerCount != 1 {
				return fmt.Errorf("long-term migration lease %s has no valid owner", id)
			}
			if err := tx.Model(&lease).Update("long_term", true).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func visibleAdminLeases(db *gorm.DB, now time.Time) ([]models.MCPLease, error) {
	var leases []models.MCPLease
	err := db.Where("created_at >= ? OR (status = ? AND revoked_at IS NULL AND expires_at > ?)", adminHistoryCutoff(now), statusActive, now).
		Order("created_at DESC").Find(&leases).Error
	return leases, err
}
