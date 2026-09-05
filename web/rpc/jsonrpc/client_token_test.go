package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
)

func TestClientWithoutTokenStripsResponseCopyOnly(t *testing.T) {
	original := sampleThemeClient(false, "keep-in-db")
	redacted := clientWithoutToken(original)
	if original.Token != "keep-in-db" {
		t.Fatalf("database copy was mutated: %q", original.Token)
	}
	if redacted.Token != "" {
		t.Fatalf("response copy still has token: %q", redacted.Token)
	}
	if redacted.Version != original.Version || redacted.Remark != original.Remark || redacted.DeploymentStatus != original.DeploymentStatus {
		t.Fatalf("admin fields should remain on list/getClient copies")
	}

	raw, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, exists := object["token"]; exists {
		t.Fatalf("token key should be omitted from list/getClient JSON: %s", raw)
	}

	list := clientsWithoutTokens([]models.Client{original})
	if list[0].Token != "" || original.Token != "keep-in-db" {
		t.Fatalf("list redaction mutated source data")
	}
}

func TestCredentialMethodsStaySensitiveAndSetNoStore(t *testing.T) {
	if !rpc.IsSensitive("admin:getClientToken") {
		t.Fatal("admin:getClientToken must be marked sensitive")
	}
	if !rpc.IsSensitive("admin:rotateClientToken") {
		t.Fatal("admin:rotateClientToken must be marked sensitive")
	}
	if rpc.IsSensitive("admin:listClients") || rpc.IsSensitive("admin:getClient") {
		t.Fatal("list/getClient must not require sensitive 2FA")
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	applyTokenResponseHeaders(ctx, "admin:getClientToken")
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("getClientToken missing no-store: %q", recorder.Header().Get("Cache-Control"))
	}

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	applyTokenResponseHeaders(ctx, "admin:rotateClientToken")
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotateClientToken missing no-store: %q", recorder.Header().Get("Cache-Control"))
	}

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	applyTokenResponseHeaders(ctx, "admin:addClient")
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("addClient missing no-store: %q", recorder.Header().Get("Cache-Control"))
	}

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	applyTokenResponseHeaders(ctx, "admin:listClients")
	if recorder.Header().Get("Cache-Control") != "" {
		t.Fatalf("listClients should not set token cache headers")
	}
	if rpc.IsSensitive("admin:addClient") {
		t.Fatal("addClient must not require sensitive 2FA")
	}
}

func TestTokenAuditLogsOmitSecret(t *testing.T) {
	source, err := os.ReadFile("admin.client.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte(`auditlog.Log(ip, actor, "view client token:"+params.UUID, "info")`)) {
		t.Fatal("view token audit log should record UUID only")
	}
	if !bytes.Contains(source, []byte(`auditlog.Log(ip, actor, "rotate client token:"+params.UUID, "warn")`)) {
		t.Fatal("rotate token audit log should record UUID only")
	}
	if bytes.Contains(source, []byte(`"view client token:"+token`)) || bytes.Contains(source, []byte("view client token:\"+token")) {
		t.Fatal("view token audit log must not include the secret")
	}
	if !bytes.Contains(source, []byte("clients.GetClientTokenByUUID")) {
		t.Fatal("getClientToken must read the stored token")
	}
	if bytes.Contains(source, []byte("func adminGetClientToken")) && bytes.Contains(source[bytes.Index(source, []byte("func adminGetClientToken")):bytes.Index(source, []byte("func adminRotateClientToken"))], []byte("CreateClient")) {
		t.Fatal("getClientToken must not create a token")
	}
	getToken := source[bytes.Index(source, []byte("func adminGetClientToken")):bytes.Index(source, []byte("func adminRotateClientToken"))]
	if bytes.Contains(getToken, []byte("RotateClientToken")) || bytes.Contains(getToken, []byte("CreateClient")) {
		t.Fatal("getClientToken must not generate or rotate tokens")
	}
}

func TestAdminGetClientOmitsTokenFromHandlerResult(t *testing.T) {
	original := sampleThemeClient(false, "db-secret")
	previousLookup := lookupAdminClient
	t.Cleanup(func() { lookupAdminClient = previousLookup })
	lookupAdminClient = func(uuid string) (models.Client, error) {
		if uuid != "node-1" {
			t.Fatalf("unexpected uuid %q", uuid)
		}
		return original, nil
	}

	result, rpcErr := adminGetClient(context.Background(), rpc.NewRequest(1, "admin:getClient", map[string]any{"uuid": "node-1"}))
	if rpcErr != nil {
		t.Fatalf("adminGetClient: %v", rpcErr)
	}
	if original.Token != "db-secret" {
		t.Fatalf("database copy was mutated: %q", original.Token)
	}
	if _, exists := jsonObjectKeys(t, result)["token"]; exists {
		raw, _ := json.Marshal(result)
		t.Fatalf("adminGetClient JSON still has token: %s", raw)
	}
	client, ok := result.(models.Client)
	if !ok {
		t.Fatalf("adminGetClient returned %T", result)
	}
	if client.Token != "" {
		t.Fatalf("response copy still has token: %q", client.Token)
	}
}

func TestAdminListClientsOmitsTokenFromHandlerResult(t *testing.T) {
	original := sampleThemeClient(false, "db-secret")
	previousList := listAdminClients
	t.Cleanup(func() { listAdminClients = previousList })
	listAdminClients = func() ([]models.Client, error) {
		return []models.Client{original}, nil
	}

	result, rpcErr := adminListClients(context.Background(), rpc.NewRequest(1, "admin:listClients", nil))
	if rpcErr != nil {
		t.Fatalf("adminListClients: %v", rpcErr)
	}
	if original.Token != "db-secret" {
		t.Fatalf("database copy was mutated: %q", original.Token)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list length %d", len(list))
	}
	if _, exists := list[0]["token"]; exists {
		t.Fatalf("adminListClients JSON still has token: %s", raw)
	}
}
