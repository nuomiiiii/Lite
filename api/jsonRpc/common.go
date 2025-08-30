package jsonRpc

import (
	"context"

	"github.com/komari-monitor/komari/utils/rpc"
)

func init() {
	Register("getNodes", func(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
		return "success", nil
	})
}
