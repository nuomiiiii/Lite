package jsonrpc

import (
	"errors"
	"net/http"
	"testing"

	"github.com/nuomiiiii/lite/database/billing"
	"github.com/nuomiiiii/lite/pkg/rpc"
)

func TestBillingInputErrorsMapToHTTPBadRequest(t *testing.T) {
	tests := []struct {
		name string
		err  *rpc.JsonRpcError
	}{
		{name: "request binding", err: invalidBillingParams(errors.New("invalid page"))},
		{name: "billing validation", err: billingRPCError(billing.ErrInvalidInput)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if status := rpcErrorHTTPStatus(test.err.Code); status != http.StatusBadRequest {
				t.Fatalf("billing error status = %d, want %d", status, http.StatusBadRequest)
			}
		})
	}
}
