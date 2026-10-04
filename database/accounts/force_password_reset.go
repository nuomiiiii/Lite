package accounts

import (
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

// Emergency password resets can run in a separate CLI process. Revocation must
// therefore be persisted atomically with the password change, not only notified
// to in-process listeners or inferred from a browser session/restart.
func forceResetPasswordWithDB(db *gorm.DB, username, hashed string) (string, error) {
	var ownerUUID string
	err := db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Where("username = ?", username).First(&user).Error; err != nil {
			return fmt.Errorf("无法找到用户名: %w", err)
		}
		ownerUUID = user.UUID
		if err := tx.Model(&user).Update("passwd", hashed).Error; err != nil {
			return err
		}
		if !tx.Migrator().HasTable(&models.MCPLease{}) {
			return nil
		}
		var leases []models.MCPLease
		if err := tx.Where("owner_user_uuid = ? AND status = ? AND revoked_at IS NULL", ownerUUID, "active").Find(&leases).Error; err != nil {
			return err
		}
		if len(leases) == 0 {
			return nil
		}
		ids := make([]string, 0, len(leases))
		for _, lease := range leases {
			ids = append(ids, lease.ID)
		}
		now := time.Now().UTC()
		if err := tx.Model(&models.MCPLease{}).Where("id IN ?", ids).Updates(map[string]any{
			"status": "revoked", "revoked_at": now, "revocation_reason": "user_security",
		}).Error; err != nil {
			return err
		}
		return tx.Model(&models.MCPToken{}).Where("lease_id IN ?", ids).Updates(map[string]any{
			"used": true, "expires_at": now,
		}).Error
	})
	return ownerUUID, err
}
