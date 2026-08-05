package jsonrpc

import (
	"context"
	"testing"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/rpc"
	"github.com/stretchr/testify/require"
)

func TestAccountPreferencePermission(t *testing.T) {
	require.True(t, rpc.CheckPermission(rpc.RoleAdmin, "admin:updateAccountPreferences"))
	require.False(t, rpc.CheckPermission(rpc.RoleGuest, "admin:updateAccountPreferences"))
}

func TestPublicGetMeReturnsAccountPreferences(t *testing.T) {
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		User: &models.User{
			UUID:     "user-1",
			Username: "admin",
			Language: "zh-CN",
			Color:    "jade",
		},
	})

	result, rpcErr := publicGetMe(ctx, &rpc.JsonRpcRequest{})
	require.Nil(t, rpcErr)
	account, ok := result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "zh-CN", account["language"])
	require.Equal(t, "jade", account["color"])
}

func TestAccountPreferenceUpdateRequiresUserSession(t *testing.T) {
	_, rpcErr := adminUpdateAccountPreferences(
		context.Background(),
		&rpc.JsonRpcRequest{Params: map[string]any{"color": "jade"}},
	)
	require.NotNil(t, rpcErr)
	require.Equal(t, rpc.PermissionDenied, rpcErr.Code)
}
