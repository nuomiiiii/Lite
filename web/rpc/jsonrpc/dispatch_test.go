package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/web/api"
)

var registerDispatchTestMethodsOnce sync.Once

func ensureDispatchTestMethods() {
	registerDispatchTestMethodsOnce.Do(func() {
		RegisterWithGroupAndMeta("testSessionEcho", rpc.RoleAdmin, func(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
			meta := rpc.MetaFromContext(ctx)
			if meta == nil {
				return nil, rpc.MakeError(rpc.PermissionDenied, "missing meta", nil)
			}
			return map[string]any{"user_uuid": meta.UserUUID}, nil
		}, nil)
		RegisterWithGroupAndMeta("testDeleteOwnSession", rpc.RoleAdmin, func(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
			meta := rpc.MetaFromContext(ctx)
			if meta != nil && meta.SessionToken != "" {
				_ = accounts.DeleteSession(meta.SessionToken)
			}
			return map[string]any{"revoked": true}, nil
		}, nil)
	})
}

func TestAdminSessionRevocationHasZeroWindowAndBatchConsistency(t *testing.T) {
	ensureDispatchTestMethods()
	gin.SetMode(gin.TestMode)

	db := dbcore.GetDBInstance()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	user := models.User{
		UUID:     uuid.NewString(),
		Username: "dispatch-rev-" + uuid.NewString()[:8],
		Passwd:   "unused",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Where("uuid = ?", user.UUID).Delete(&models.Session{}).Error
		_ = db.Where("uuid = ?", user.UUID).Delete(&models.User{}).Error
	})

	sessionToken, err := accounts.CreateSession(user.UUID, 3600, "test-ua", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	engine.Use(api.IdentityMiddleware())
	engine.POST("/api/rpc2", OnRpcRequest)

	postRPC := func(tok string, payload any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/rpc2", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "session_token", Value: tok})
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}

	// 1. Live session succeeds.
	okRec := postRPC(sessionToken, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "admin:testSessionEcho",
	})
	if okRec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", okRec.Code, okRec.Body.String())
	}
	var okResp rpc.JsonRpcResponse
	if err := json.Unmarshal(okRec.Body.Bytes(), &okResp); err != nil {
		t.Fatal(err)
	}
	if okResp.Error != nil {
		t.Fatalf("unexpected error for active session: %+v", okResp.Error)
	}

	// 2. Batch RPC where item #0 revokes the session: item #1 in the SAME HTTP batch must fail immediately.
	batchRec := postRPC(sessionToken, []map[string]any{
		{"jsonrpc": "2.0", "id": 10, "method": "admin:testDeleteOwnSession"},
		{"jsonrpc": "2.0", "id": 11, "method": "admin:testSessionEcho"},
	})
	if batchRec.Code != http.StatusOK {
		t.Fatalf("batch status = %d, body = %s", batchRec.Code, batchRec.Body.String())
	}
	var batchResps []rpc.JsonRpcResponse
	if err := json.Unmarshal(batchRec.Body.Bytes(), &batchResps); err != nil {
		t.Fatal(err)
	}
	if len(batchResps) != 2 {
		t.Fatalf("batch len = %d, want 2", len(batchResps))
	}
	if batchResps[0].Error != nil {
		t.Fatalf("batch[0] unexpected error: %+v", batchResps[0].Error)
	}
	if batchResps[1].Error == nil || batchResps[1].Error.Code != rpc.PermissionDenied {
		t.Fatalf("batch[1] after session deletion expected PermissionDenied, got %+v", batchResps[1].Error)
	}

	// 3. Immediate next HTTP call (0ms after logout/delete) with a fresh session deleted right before call must fail.
	session2, err := accounts.CreateSession(user.UUID, 3600, "test-ua", "127.0.0.1", "password")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.DeleteSession(session2); err != nil {
		t.Fatal(err)
	}
	afterLogoutRec := postRPC(session2, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "admin:testSessionEcho",
	})
	var afterLogoutResp rpc.JsonRpcResponse
	if err := json.Unmarshal(afterLogoutRec.Body.Bytes(), &afterLogoutResp); err != nil {
		t.Fatal(err)
	}
	if afterLogoutResp.Error == nil || afterLogoutResp.Error.Code != rpc.PermissionDenied {
		t.Fatalf("immediate call after DeleteSession expected PermissionDenied, got %+v", afterLogoutResp.Error)
	}
}

// TestPrivateSiteLoginWhitelist 守卫 issue #567:私有站点模式下,登录页渲染所需的
// 元信息接口必须始终在白名单中,否则匿名用户无法看到登录框。
func TestPrivateSiteLoginWhitelist(t *testing.T) {
	required := []string{
		"public:getMe",
		"public:getPublicSettings",
		"public:getVersion",
		"public:recordVisitorEvent",
	}
	for _, m := range required {
		if !privateSiteLoginWhitelist[m] {
			t.Errorf("method %q must be in privateSiteLoginWhitelist for login page to render under private site (issue #567)", m)
		}
	}

	// 节点列表等数据接口不应在白名单(应被私有站点拦截)。
	mustBlocked := []string{
		"public:getNodesInformation",
		"public:getRecordsByUUID",
		"public:getPingRecords",
	}
	for _, m := range mustBlocked {
		if privateSiteLoginWhitelist[m] {
			t.Errorf("data method %q must NOT be in privateSiteLoginWhitelist (would leak data under private site)", m)
		}
	}
}
