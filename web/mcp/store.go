package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/web/api/remote"
	"github.com/nuomiiiii/lite/web/remotectl"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	statusPending = "pending"
	statusActive  = "active"
	statusRevoked = "revoked"
	statusExpired = "expired"
	statusDenied  = "denied"

	kindAccess  = "access"
	kindRefresh = "refresh"
	kindCode    = "code"

	opAccepted        = "accepted"
	opRunning         = "running"
	opSucceeded       = "succeeded"
	opFailed          = "failed"
	opCancelRequested = "cancel_requested"
	opCancelled       = "cancelled"
	opExpired         = "expired"
	opUnknown         = "unknown"

	reasonRestart      = "server_restart"
	reasonRevoked      = "revoked"
	reasonUserSecurity = "user_security"

	adminHistoryRetention    = 3 * 24 * time.Hour
	operationOutputRetention = 15 * time.Minute
	outputCompactMinInterval = time.Minute
	reasonLoginRevoked       = "login_session"
	reasonMCPOff             = "mcp_disabled"
	reasonRemoteOff          = "remote_disabled"
	reasonRefreshReuse       = "refresh_reuse"
)

var (
	database = dbcore.GetDBInstance

	outputCompactMu   sync.Mutex
	lastOutputCompact time.Time
)

func init() {
	remote.SetMCPTerminalLeaseValidator(func(leaseID, userUUID, nodeUUID string) bool {
		lease, err := loadLiveLease(leaseID, time.Now().UTC())
		return err == nil && lease.OwnerUserUUID == userUUID && leaseContainsNode(lease, nodeUUID)
	})
	accounts.AddUserSecurityListener(RevokeUser)
	remotectl.AddRevokeAllListener(func() {
		_ = revokeActiveLeases("", "", reasonRemoteOff)
	})
	remotectl.AddRevokeLoginListener(func(loginSession string) {
		_ = revokeActiveLeases("", accounts.SessionLookupKey(loginSession), reasonLoginRevoked)
	})
}

func InvalidateActiveLeases() error {
	promotionErr := promoteConfiguredLongTermLeases()
	return errors.Join(promotionErr, revokeActiveLeases("", "", reasonRestart))
}

func RevokeUser(userUUID string) {
	_ = revokeActiveLeases(userUUID, "", reasonUserSecurity)
}

func RevokeAllActive(reason string) error {
	if reason == "" {
		reason = reasonRevoked
	}
	return revokeActiveLeases("", "", reason)
}

func revokeActiveLeases(userUUID, loginHash, reason string) error {
	db := database()
	now := time.Now().UTC()
	query := db.Model(&models.MCPLease{}).Where("status = ? AND revoked_at IS NULL", statusActive)
	if reason == reasonRestart || reason == reasonLoginRevoked {
		query = query.Where("long_term = ?", false)
	}
	if userUUID != "" {
		query = query.Where("owner_user_uuid = ?", userUUID)
	}
	if loginHash != "" {
		query = query.Where("owner_login_session_hash = ?", loginHash)
	}
	var leases []models.MCPLease
	if err := query.Find(&leases).Error; err != nil {
		return err
	}
	if len(leases) == 0 {
		return nil
	}
	ids := make([]string, 0, len(leases))
	families := make([]string, 0, len(leases))
	for _, lease := range leases {
		ids = append(ids, lease.ID)
		families = append(families, lease.TokenFamilyID)
		remote.CloseMCPLeaseSessions(lease.ID)
		cancelLeaseOperations(lease.ID)
	}
	if err := db.Model(&models.MCPLease{}).Where("id IN ?", ids).Updates(map[string]any{
		"status":            statusRevoked,
		"revoked_at":        now,
		"revocation_reason": reason,
	}).Error; err != nil {
		return err
	}
	_ = compactLeaseOperationOutputs(db, ids...)
	return db.Model(&models.MCPToken{}).Where("family_id IN ?", families).Updates(map[string]any{
		"used":       true,
		"expires_at": now,
	}).Error
}

func parseTargetUUIDs(raw string) []string {
	var ids []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &ids); err != nil {
		return compactStrings(strings.Split(raw, ","))
	}
	return compactStrings(ids)
}

func encodeTargetUUIDs(ids []string) string {
	return encodeJSONList(ids)
}

func leaseLive(lease models.MCPLease, now time.Time) bool {
	if lease.Status != statusActive || lease.RevokedAt != nil {
		return false
	}
	return lease.ExpiresAt.After(now)
}

func loadLiveLease(id string, now time.Time) (models.MCPLease, error) {
	var lease models.MCPLease
	if err := database().Where("id = ?", id).First(&lease).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.MCPLease{}, ErrLeaseInactive
		}
		return models.MCPLease{}, err
	}
	if !leaseLive(lease, now) {
		if lease.Status == statusActive && !lease.ExpiresAt.After(now) {
			_ = database().Model(&lease).Updates(map[string]any{
				"status":            statusExpired,
				"revocation_reason": statusExpired,
			}).Error
			_ = compactLeaseOperationOutputs(database(), lease.ID)
		}
		return models.MCPLease{}, ErrLeaseInactive
	}
	if lease.LongTerm {
		if !longTermOwnerExists(lease.OwnerUserUUID) {
			_ = revokeActiveLeases(lease.OwnerUserUUID, "", reasonUserSecurity)
			return models.MCPLease{}, ErrLeaseInactive
		}
	} else if !accounts.SessionStillValid(lease.OwnerUserUUID, lease.OwnerLoginSessionHash) {
		_ = revokeActiveLeases(lease.OwnerUserUUID, lease.OwnerLoginSessionHash, reasonLoginRevoked)
		return models.MCPLease{}, ErrLeaseInactive
	}
	return lease, nil
}

func leaseContainsNode(lease models.MCPLease, uuid string) bool {
	uuid = strings.TrimSpace(uuid)
	for _, item := range parseTargetUUIDs(lease.TargetUUIDs) {
		if item == uuid {
			return true
		}
	}
	return false
}

func agentSupportsMCP(client models.Client) bool {
	return client.RemoteControlEnabled && client.MCPFull && client.MCPFullVersion >= v2.MCPFullVersion
}

func countActiveLeases(userUUID string) (global int, user int, err error) {
	db := database()
	now := time.Now().UTC()
	var globalCount int64
	if err = db.Model(&models.MCPLease{}).
		Where("status = ? AND expires_at > ? AND revoked_at IS NULL", statusActive, now).
		Count(&globalCount).Error; err != nil {
		return 0, 0, err
	}
	var userCount int64
	if err = db.Model(&models.MCPLease{}).
		Where("status = ? AND expires_at > ? AND revoked_at IS NULL AND owner_user_uuid = ?", statusActive, now, userUUID).
		Count(&userCount).Error; err != nil {
		return 0, 0, err
	}
	return int(globalCount), int(userCount), nil
}

func saveClient(client models.MCPClient) error {
	return database().Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Create(&client).Error
}

func loadClient(id string) (models.MCPClient, error) {
	var client models.MCPClient
	err := database().Where("client_id = ?", id).First(&client).Error
	return client, err
}

func adminHistoryCutoff(now time.Time) time.Time {
	return now.Add(-adminHistoryRetention)
}

// CleanupHistory drops MCP authorizations, operations, tokens, and pending
// requests older than the 3-day admin history window.
func CleanupHistory() error {
	return cleanupHistory(database(), time.Now().UTC())
}

func purgeInactiveHistory(db *gorm.DB, now time.Time) (int, error) {
	var leases []models.MCPLease
	if err := db.Where(
		"status IN ? OR (status = ? AND expires_at <= ?)",
		[]string{statusDenied, statusExpired, statusRevoked},
		statusActive,
		now,
	).Find(&leases).Error; err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(leases))
	families := make([]string, 0, len(leases))
	for _, lease := range leases {
		if leaseLive(lease, now) {
			continue
		}
		ids = append(ids, lease.ID)
		families = append(families, lease.TokenFamilyID)
		remote.CloseMCPLeaseSessions(lease.ID)
	}
	if len(ids) > 0 {
		if err := db.Where("lease_id IN ?", ids).Delete(&models.MCPOperation{}).Error; err != nil {
			return 0, err
		}
		if err := db.Where("lease_id IN ?", ids).Delete(&models.MCPToken{}).Error; err != nil {
			return 0, err
		}
		if err := db.Where("family_id IN ?", families).Delete(&models.MCPToken{}).Error; err != nil {
			return 0, err
		}
		if err := db.Where("id IN ?", ids).Delete(&models.MCPLease{}).Error; err != nil {
			return 0, err
		}
	}
	if err := db.Where("status = ?", statusDenied).Delete(&models.MCPAuthorizationRequest{}).Error; err != nil {
		return 0, err
	}
	if err := compactStaleOperationOutputs(db, now); err != nil {
		return len(ids), err
	}
	return len(ids), nil
}

func recordDeniedAuthorization(db *gorm.DB, req models.MCPAuthorizationRequest, ownerUUID, loginHash string, now time.Time) (models.MCPLease, error) {
	leaseID, err := newID("ls_")
	if err != nil {
		return models.MCPLease{}, err
	}
	familyID, err := newID("tf_")
	if err != nil {
		return models.MCPLease{}, err
	}
	lease := models.MCPLease{
		ID:                     leaseID,
		OwnerUserUUID:          ownerUUID,
		OwnerLoginSessionHash:  loginHash,
		OAuthClientID:          req.ClientID,
		AuthorizationRequestID: req.ID,
		TokenFamilyID:          familyID,
		TargetUUIDs:            encodeTargetUUIDs(nil),
		Mode:                   modeFull,
		MaxConcurrency:         0,
		Status:                 statusDenied,
		PolicyVersion:          PolicyVersion,
		CreatedAt:              now,
		ExpiresAt:              now,
		RevocationReason:       statusDenied,
	}
	if err := db.Create(&lease).Error; err != nil {
		return models.MCPLease{}, err
	}
	if err := db.Model(&req).Updates(map[string]any{
		"status":          statusDenied,
		"owner_user_uuid": ownerUUID,
	}).Error; err != nil {
		return lease, err
	}
	return lease, nil
}

func cleanupHistory(db *gorm.DB, now time.Time) error {
	cutoff := adminHistoryCutoff(now)
	if err := db.Where("created_at < ?", cutoff).Delete(&models.MCPOperation{}).Error; err != nil {
		return err
	}
	if err := db.Where("created_at < ?", cutoff).Delete(&models.MCPAuthorizationRequest{}).Error; err != nil {
		return err
	}
	var leases []models.MCPLease
	if err := db.Where("created_at < ?", cutoff).Find(&leases).Error; err != nil {
		return err
	}
	ids := make([]string, 0, len(leases))
	families := make([]string, 0, len(leases))
	for _, lease := range leases {
		if leaseLive(lease, now) {
			continue
		}
		ids = append(ids, lease.ID)
		families = append(families, lease.TokenFamilyID)
		remote.CloseMCPLeaseSessions(lease.ID)
	}
	if len(ids) > 0 {
		if err := db.Where("lease_id IN ?", ids).Delete(&models.MCPToken{}).Error; err != nil {
			return err
		}
		if err := db.Where("family_id IN ?", families).Delete(&models.MCPToken{}).Error; err != nil {
			return err
		}
		if err := db.Where("id IN ?", ids).Delete(&models.MCPLease{}).Error; err != nil {
			return err
		}
	}
	if err := db.Where("created_at < ? AND (used = ? OR expires_at <= ?)", cutoff, true, now).Delete(&models.MCPToken{}).Error; err != nil {
		return err
	}
	if err := compactStaleOperationOutputs(db, now); err != nil {
		return err
	}
	return pruneUnusedMCPClients(db, now)
}

func pruneUnusedMCPClients(db *gorm.DB, now time.Time) error {
	if !db.Migrator().HasTable(&models.MCPClient{}) {
		return nil
	}
	stale := now.Add(-24 * time.Hour)
	return db.Exec(`
		DELETE FROM mcp_clients
		WHERE created_at < ?
		AND NOT EXISTS (SELECT 1 FROM mcp_leases WHERE mcp_leases.o_auth_client_id = mcp_clients.client_id)
		AND NOT EXISTS (SELECT 1 FROM mcp_tokens WHERE mcp_tokens.client_id = mcp_clients.client_id)
	`, stale).Error
}

func unusedMCPClientCount() int64 {
	var count int64
	_ = database().Raw(`
		SELECT COUNT(*) FROM mcp_clients
		WHERE NOT EXISTS (SELECT 1 FROM mcp_leases WHERE mcp_leases.o_auth_client_id = mcp_clients.client_id)
		AND NOT EXISTS (SELECT 1 FROM mcp_tokens WHERE mcp_tokens.client_id = mcp_clients.client_id)
	`).Scan(&count).Error
	return count
}

func maybeCompactOperationOutputs() {
	outputCompactMu.Lock()
	defer outputCompactMu.Unlock()
	now := time.Now().UTC()
	if !lastOutputCompact.IsZero() && now.Sub(lastOutputCompact) < outputCompactMinInterval {
		return
	}
	if err := compactStaleOperationOutputs(database(), now); err != nil {
		return
	}
	lastOutputCompact = now
}

func compactStaleOperationOutputs(db *gorm.DB, now time.Time) error {
	if db == nil {
		return nil
	}
	keepFullAfter := now.Add(-operationOutputRetention)
	return db.Exec(`
		UPDATE mcp_operations
		SET
			output = substr(output, 1, ?),
			truncated = CASE WHEN length(output) > ? THEN 1 ELSE truncated END
		WHERE length(output) > ?
		  AND (
		    lease_id NOT IN (
		      SELECT id FROM mcp_leases
		      WHERE status = ? AND revoked_at IS NULL AND expires_at > ?
		    )
		    OR (finished_at IS NOT NULL AND finished_at <= ?)
		  )
	`, AdminOutputPreviewMax, AdminOutputPreviewMax, AdminOutputPreviewMax, statusActive, now, keepFullAfter).Error
}

func compactLeaseOperationOutputs(db *gorm.DB, leaseIDs ...string) error {
	if db == nil || len(leaseIDs) == 0 {
		return nil
	}
	return db.Exec(`
		UPDATE mcp_operations
		SET
			output = substr(output, 1, ?),
			truncated = CASE WHEN length(output) > ? THEN 1 ELSE truncated END
		WHERE length(output) > ? AND lease_id IN ?
	`, AdminOutputPreviewMax, AdminOutputPreviewMax, AdminOutputPreviewMax, leaseIDs).Error
}
