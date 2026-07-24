package client

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientUUIDComesFromAuthenticatedContext(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("POST", "/api/clients/report?token=untrusted-query", nil)
	context.Set("client_uuid", "authenticated-node")

	uuid, ok := clientUUIDFromContext(context)
	if !ok || uuid != "authenticated-node" {
		t.Fatalf("authenticated node was not authoritative: uuid=%q ok=%v", uuid, ok)
	}
}
