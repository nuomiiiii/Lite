package jsonrpc

import (
	"context"

	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/pkg/rpc"
)

func init() {
	RegisterWithGroupAndMeta("updateAccountPreferences", rpc.RoleAdmin, adminUpdateAccountPreferences, &rpc.MethodMeta{
		Name:    "admin:updateAccountPreferences",
		Summary: "Update the current administrator's language and color preferences",
		Params: []rpc.ParamMeta{
			{Name: "language", Type: "string", Description: "Administrator UI language"},
			{Name: "color", Type: "string", Description: "Administrator accent color"},
		},
		Returns: "null",
	})
}

func adminUpdateAccountPreferences(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Language *string `json:"language"`
		Color    *string `json:"color"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid preference request", nil)
	}

	meta := rpc.MetaFromContext(ctx)
	if meta == nil || meta.User == nil || meta.User.UUID == "" {
		return nil, rpc.MakeError(rpc.PermissionDenied, "An administrator account session is required", nil)
	}
	if err := accounts.UpdateUserPreferences(meta.User.UUID, params.Language, params.Color); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return nil, nil
}
