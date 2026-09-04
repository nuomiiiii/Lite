package client

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientUUIDComesFromAuthenticatedContext(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("POST", "/api/clients/v2/rpc?token=untrusted-query", nil)
	context.Set("client_uuid", "authenticated-node")

	uuid, ok := clientUUIDFromContext(context)
	if !ok || uuid != "authenticated-node" {
		t.Fatalf("authenticated node was not authoritative: uuid=%q ok=%v", uuid, ok)
	}
}

func TestClientUUIDIgnoresQueryToken(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("POST", "/api/clients/v2/rpc?token=untrusted-query", nil)

	uuid, ok := clientUUIDFromContext(context)
	if ok || uuid != "" {
		t.Fatalf("query token was accepted: uuid=%q ok=%v", uuid, ok)
	}
}
