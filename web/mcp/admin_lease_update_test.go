package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/api/remote"
	"gorm.io/gorm"
)

func TestAllNodesIsExplicitAndIncludesFutureNodes(t *testing.T) {
	lease := models.MCPLease{TargetUUIDs: `["existing"]`}
	if !leaseContainsNode(lease, "existing") || leaseContainsNode(lease, "future") {
		t.Fatal("legacy explicit selection changed")
	}
	lease.AllNodes = true
	if !leaseContainsNode(lease, "future") || leaseContainsNode(lease, " ") {
		t.Fatal("dynamic scope must allow future nodes but not empty IDs")
	}
	targets, err := validateTargetSelection(true, []string{"stale"})
	if err != nil || len(targets) != 0 {
		t.Fatalf("dynamic scope stored a snapshot: %v %v", targets, err)
	}
	if _, err := validateTargetSelection(false, nil); err == nil {
		t.Fatal("an empty explicit selection must not mean all nodes")
	}
	payload := authorizationPayload(lease, time.Now())
	if payload["all_nodes"] != true {
		t.Fatal("MCP client cannot distinguish dynamic from explicit scope")
	}
}

func TestAllNodesMigrationDoesNotExpandExistingLeases(t *testing.T) {
	db := longTermTestDB(t)
	if err := db.Migrator().DropColumn(&models.MCPLease{}, "AllNodes"); err != nil {
		t.Fatal(err)
	}
	lease := longTermTestLease("legacy", true, time.Now().UTC())
	if err := db.Omit("AllNodes").Create(&lease).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.MCPLease{}); err != nil {
		t.Fatal(err)
	}
	got := longTermTestStoredLease(t, db, lease.ID)
	if got.AllNodes || got.TargetUUIDs != lease.TargetUUIDs {
		t.Fatalf("migration expanded legacy scope: %+v", got)
	}
}

func TestSaveLeasePolicyUpdatesUsableRefreshTokensOnly(t *testing.T) {
	for _, longTerm := range []bool{false, true} {
		t.Run(map[bool]string{false: "shorten", true: "extend-to-long-term"}[longTerm], func(t *testing.T) {
			db := longTermTestDB(t)
			now := time.Now().UTC()
			lease := longTermTestLease("editable", true, now)
			lease.ExpiresAt = now.Add(time.Hour)
			longTermTestCreate(t, db, &lease)
			for _, token := range []models.MCPToken{
				{Hash: "usable", Kind: kindRefresh, ExpiresAt: lease.ExpiresAt},
				{Hash: "used", Kind: kindRefresh, Used: true, ExpiresAt: lease.ExpiresAt},
				{Hash: "expired", Kind: kindRefresh, ExpiresAt: now.Add(-time.Minute)},
				{Hash: "access", Kind: kindAccess, ExpiresAt: now.Add(DefaultAccessTokenTTL)},
				{Hash: "code", Kind: kindCode, ExpiresAt: now.Add(AuthCodeTTL)},
			} {
				token.LeaseID, token.FamilyID = lease.ID, lease.TokenFamilyID
				longTermTestCreate(t, db, &token)
			}
			var original []models.MCPToken
			if err := db.Order("hash").Find(&original).Error; err != nil {
				t.Fatal(err)
			}
			lease.LongTerm, lease.AllNodes = longTerm, true
			lease.TargetUUIDs, lease.Note = "[]", "updated"
			lease.ExpiresAt = now.Add(30 * time.Minute)
			if longTerm {
				lease.ExpiresAt = longTermExpiresAt()
			}
			if err := saveLeasePolicy(db, lease, now); err != nil {
				t.Fatal(err)
			}
			got := longTermTestStoredLease(t, db, lease.ID)
			if !got.AllNodes || got.TargetUUIDs != "[]" || got.LongTerm != longTerm ||
				!got.ExpiresAt.Equal(lease.ExpiresAt) || got.Note != "updated" || !got.CreatedAt.Equal(lease.CreatedAt) {
				t.Fatalf("policy not saved: %+v", got)
			}
			for _, old := range original {
				var token models.MCPToken
				if err := db.Where("hash = ?", old.Hash).First(&token).Error; err != nil {
					t.Fatal(err)
				}
				want := old.ExpiresAt
				if old.Hash == "usable" {
					want = lease.ExpiresAt
				}
				if !token.ExpiresAt.Equal(want) || token.Used != old.Used {
					t.Fatalf("unexpected token mutation: %+v, want expiry %s", token, want)
				}
			}
			// All-nodes can be narrowed again without leaving the flag enabled.
			lease.AllNodes, lease.TargetUUIDs = false, `["node-b"]`
			if err := saveLeasePolicy(db, lease, now); err != nil {
				t.Fatal(err)
			}
			got = longTermTestStoredLease(t, db, lease.ID)
			if got.AllNodes || leaseContainsNode(got, "node-a") || !leaseContainsNode(got, "node-b") {
				t.Fatal("narrowing did not remove access")
			}
		})
	}
}

func TestSaveLeasePolicyCannotReviveEndedAuthorization(t *testing.T) {
	for _, status := range []string{statusExpired, statusRevoked, statusDenied, statusActive} {
		t.Run(status, func(t *testing.T) {
			db := longTermTestDB(t)
			now := time.Now().UTC()
			lease := longTermTestLease("ended", true, now)
			lease.Status = status
			lease.ExpiresAt = now.Add(-time.Minute)
			longTermTestCreate(t, db, &lease)
			lease.ExpiresAt = longTermExpiresAt()
			if err := saveLeasePolicy(db, lease, now); !errors.Is(err, ErrLeaseInactive) {
				t.Fatalf("ended policy update = %v", err)
			}
			if got := longTermTestStoredLease(t, db, lease.ID); got.ExpiresAt.After(now) {
				t.Fatal("ended authorization revived")
			}
		})
	}
}

func TestSaveLeasePolicyRollsBackOnTokenUpdateFailure(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Now().UTC()
	lease := longTermTestLease("atomic", true, now)
	longTermTestCreate(t, db, &lease)
	name := "test:reject-token-policy-update"
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "mcp_tokens" {
			tx.AddError(errors.New("injected token write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(name) })
	lease.AllNodes = true
	if err := saveLeasePolicy(db, lease, now); err == nil {
		t.Fatal("expected transaction failure")
	}
	if got := longTermTestStoredLease(t, db, lease.ID); got.AllNodes {
		t.Fatal("lease policy committed without its refresh-token update")
	}
}

func TestExtendedLeaseRefreshWorksBeyondOriginalExpiry(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Now().UTC()
	issuedAt := now.Add(-2 * time.Minute)
	lease := longTermTestLease("extended", true, issuedAt)
	lease.ExpiresAt = issuedAt.Add(time.Minute)
	longTermTestCreate(t, db, &lease)
	pair, err := issueTokenPair(lease, "client", "https://client.invalid/callback", "https://lite.invalid/mcp", issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an edit while the original authorization was still live.
	lease.ExpiresAt = longTermExpiresAt()
	if err := saveLeasePolicy(db, lease, issuedAt.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	status, body := longTermTestRefreshHTTP(t, pair.Refresh, "client")
	if status != http.StatusOK || body["refresh_token"] == nil {
		t.Fatalf("extended refresh failed after original expiry: %d %+v", status, body)
	}
	var successor models.MCPToken
	if err := db.Where("hash = ?", hashToken(body["refresh_token"].(string))).First(&successor).Error; err != nil {
		t.Fatal(err)
	}
	if !successor.ExpiresAt.Equal(lease.ExpiresAt) {
		t.Fatalf("rotation did not use edited expiry: %s", successor.ExpiresAt)
	}
}

func TestEditStopsWorkWithoutPermanentlyRevokingLeaseAtAgent(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Now().UTC()
	lease := longTermTestLease("editing", true, now)
	longTermTestCreate(t, db, &lease)
	uuid := "edit-test-node"
	t.Cleanup(func() { agent_runtime.RemoveV2EventQueue(uuid) })
	longTermTestCreate(t, db, &models.MCPOperation{
		ID: "edit-op", LeaseID: lease.ID, AgentUUID: uuid, State: opRunning,
		CreatedAt: now, Deadline: now.Add(time.Minute),
	})
	stopLeaseOperations(lease.ID, false)
	events := agent_runtime.TakeV2Events(uuid, nil, 10)
	if len(events) != 1 || events[0].Method != v2.MethodAgentMCPCancel {
		t.Fatalf("edit must cancel work, not revoke reusable lease ID: %+v", events)
	}
	op, err := loadOperation(lease.ID, "edit-op")
	if err != nil || op.FinishedAt == nil || op.State != opCancelled || runningOperationCount(lease.ID) != 0 {
		t.Fatalf("old work did not stop: %+v %v", op, err)
	}
	// Revoking an all-nodes grant must reach nodes used by completed operations,
	// even though the policy has no stored target snapshot.
	lease.AllNodes, lease.TargetUUIDs = true, "[]"
	if err := db.Save(&lease).Error; err != nil {
		t.Fatal(err)
	}
	cancelLeaseOperations(lease.ID)
	events = agent_runtime.TakeV2Events(uuid, nil, 10)
	found := false
	for _, event := range events {
		found = found || event.Method == v2.MethodAgentMCPRevoke
	}
	if !found {
		t.Fatal("dynamic grant revocation missed previously used node")
	}
}

func TestLeaseEditRejectsNonSessionAndOtherOwners(t *testing.T) {
	db := longTermTestDB(t)
	longTermTestCreate(t, db, longTermTestLeasePtr("owned", time.Now().UTC()))
	for _, tc := range []struct {
		name      string
		principal *rpc.Principal
		cookie    bool
		origin    string
	}{
		{"api-key", rpc.NewAPIKeyPrincipal(), true, "http://127.0.0.1:27777"},
		{"missing-cookie", rpc.NewUserPrincipal("owner"), false, "http://127.0.0.1:27777"},
		{"other-owner", rpc.NewUserPrincipal("other"), true, "http://127.0.0.1:27777"},
		{"cross-origin", rpc.NewUserPrincipal("owner"), true, "https://evil.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:27777/api/admin/mcp/leases/owned", strings.NewReader(`{"all_nodes":true}`))
			c.Request.Header.Set("Origin", tc.origin)
			c.Request.Header.Set("Content-Type", "application/json")
			if tc.cookie {
				c.Request.AddCookie(&http.Cookie{Name: "session_token", Value: "browser"})
			}
			c.Params = gin.Params{{Key: "id", Value: "owned"}}
			api.SetPrincipal(c, tc.principal)
			updateLease(c)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func longTermTestLeasePtr(id string, now time.Time) *models.MCPLease {
	lease := longTermTestLease(id, true, now)
	return &lease
}

func TestApplyLeaseEditOmitsKeepNoteAndLongTerm(t *testing.T) {
	now := time.Now().UTC()
	lease := longTermTestLease("edit-omit", true, now)
	lease.Note = "keep-me"
	lease.AllNodes = true
	lease.TargetUUIDs = "[]"
	got, err := applyLeaseEdit(lease, leaseEditRequest{DurationMinutes: 90}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "keep-me" || !got.LongTerm || !got.ExpiresAt.Equal(lease.ExpiresAt) || !got.AllNodes {
		t.Fatalf("omitted note/long_term mutated grant: %+v", got)
	}
	if leaseWorkMustStop(lease, got) {
		t.Fatal("keeping long-term expiry and all-nodes must not interrupt work")
	}
	note := "  updated  "
	got, err = applyLeaseEdit(lease, leaseEditRequest{Note: &note}, now)
	if err != nil || got.Note != "updated" || leaseWorkMustStop(lease, got) {
		t.Fatalf("note-only edit should not interrupt: %+v %v", got, err)
	}
	got, err = applyLeaseEdit(lease, leaseEditRequest{LongTerm: boolPtr(false), DurationMinutes: 30}, now)
	if err != nil || got.LongTerm || !leaseWorkMustStop(lease, got) {
		t.Fatalf("explicit short-term switch must interrupt: %+v %v", got, err)
	}
}

func TestApplyLeaseEditOmitsKeepServerScope(t *testing.T) {
	now := time.Now().UTC()
	lease := longTermTestLease("edit-scope", true, now)
	lease.AllNodes = false
	lease.TargetUUIDs = `["node-a","node-b"]`
	note := "keep-scope"
	got, err := applyLeaseEdit(lease, leaseEditRequest{Note: &note}, now)
	if err != nil || got.AllNodes || got.TargetUUIDs != lease.TargetUUIDs || got.Note != "keep-scope" {
		t.Fatalf("omitted scope mutated grant: %+v %v", got, err)
	}
	if _, err := applyLeaseEdit(lease, leaseEditRequest{TargetUUIDs: stringSlicePtr(nil)}, now); err == nil {
		t.Fatal("empty explicit list must not become all servers")
	}
	got, err = applyLeaseEdit(lease, leaseEditRequest{AllNodes: boolPtr(false)}, now)
	if err != nil || got.AllNodes || got.TargetUUIDs != lease.TargetUUIDs {
		t.Fatalf("all_nodes false without a list should keep the allowlist: %+v %v", got, err)
	}
	got, err = applyLeaseEdit(lease, leaseEditRequest{AllNodes: boolPtr(true)}, now)
	if err != nil || !got.AllNodes || got.TargetUUIDs != "[]" {
		t.Fatalf("explicit all-nodes should replace the allowlist: %+v %v", got, err)
	}
	if _, err := applyLeaseEdit(got, leaseEditRequest{AllNodes: boolPtr(false)}, now); err == nil {
		t.Fatal("leaving all-nodes without a replacement list must be rejected")
	}
}

func TestLeaseWorkMustStopOnlyOnScopeOrExpiry(t *testing.T) {
	now := time.Now().UTC()
	base := longTermTestLease("scope", false, now)
	base.ExpiresAt = now.Add(time.Hour)
	same := base
	same.TargetUUIDs = `["node-b","node-a"]`
	same.Note = "other"
	if leaseWorkMustStop(base, same) {
		t.Fatal("same UUID set in another order is not a scope change")
	}
	narrow := base
	narrow.TargetUUIDs = `["node-a"]`
	if !leaseWorkMustStop(base, narrow) {
		t.Fatal("removing a node must interrupt")
	}
	wildcard := base
	wildcard.AllNodes, wildcard.TargetUUIDs = true, "[]"
	if !leaseWorkMustStop(base, wildcard) {
		t.Fatal("switching to all-nodes must interrupt")
	}
}

func TestInterruptLeaseWorkDropsQueuedExecAndClosesTerminals(t *testing.T) {
	db := longTermTestDB(t)
	now := time.Now().UTC()
	lease := longTermTestLease("editing-queue", true, now)
	longTermTestCreate(t, db, &lease)
	uuid := "edit-queue-node"
	t.Cleanup(func() {
		agent_runtime.RemoveV2EventQueue(uuid)
		remote.CloseMCPLeaseSessions(lease.ID)
	})
	agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentMCPExec, v2.MCPExecParams{
		TaskID: "queued-exec", LeaseID: lease.ID, OperationID: "queued-exec", AgentUUID: uuid,
		ExpiresAt: lease.ExpiresAt,
	})
	agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentMCPExec, v2.MCPExecParams{
		TaskID: "other-exec", LeaseID: "other-lease", OperationID: "other-exec", AgentUUID: uuid,
		ExpiresAt: lease.ExpiresAt,
	})
	removed := interruptLeaseWork(lease.ID)
	if len(removed) != 1 || agent_runtime.MCPEventLeaseID(removed[0].Event) != lease.ID {
		t.Fatalf("queued work for this grant was not dropped: %+v", removed)
	}
	events := agent_runtime.TakeV2Events(uuid, nil, 10)
	if len(events) != 1 || events[0].Method != v2.MethodAgentMCPExec || agent_runtime.MCPTaskID(events[0]) != "other-exec" {
		t.Fatalf("unrelated queued work changed: %+v", events)
	}
}

func TestMCPDispatchMatchesLeaseAfterExpiryExtension(t *testing.T) {
	now := time.Now().UTC()
	lease := models.MCPLease{AllNodes: true, ExpiresAt: now.Add(2 * time.Hour)}
	old := now.Add(time.Hour)
	if !mcpDispatchMatchesLease(lease, "future-node", old) {
		t.Fatal("extended grant must still deliver work issued with the earlier expiry")
	}
	if mcpDispatchMatchesLease(lease, "future-node", now.Add(3*time.Hour)) {
		t.Fatal("work claiming to outlive the grant must be rejected")
	}
	if mcpDispatchMatchesLease(lease, "future-node", time.Time{}) {
		t.Fatal("work without an expiry must be rejected")
	}
	lease.AllNodes = false
	lease.TargetUUIDs = `["kept"]`
	if mcpDispatchMatchesLease(lease, "removed", old) {
		t.Fatal("narrowed grant must not match a removed node")
	}
}

func TestEmbeddedAdminReauthorizationStillDecodesCredentials(t *testing.T) {
	var body struct {
		adminReauthorization
		AllNodes bool `json:"all_nodes"`
	}
	if err := json.Unmarshal([]byte(`{"password":"test","otp":"123456","ceremony_id":"ceremony","credential":{"id":"key"},"all_nodes":true}`), &body); err != nil {
		t.Fatal(err)
	}
	if body.Password != "test" || body.OTP != "123456" || body.CeremonyID != "ceremony" || len(body.Credential) == 0 || !body.AllNodes {
		t.Fatalf("shared reauthorization fields did not decode: %+v", body)
	}
}
