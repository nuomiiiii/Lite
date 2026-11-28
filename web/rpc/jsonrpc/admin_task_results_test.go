package jsonrpc

import (
	"context"
	"testing"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/stretchr/testify/require"
)

func adminTaskTestContext() context.Context {
	return rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		Principal:    &rpc.Principal{Type: rpc.PrincipalUser, UserUUID: "admin"},
		SessionToken: "session",
		UserUUID:     "admin",
	})
}

func TestAdminGetTaskResultsByTaskIdFilterEmptyIsArrayNot404(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:admin_task_filter?mode=memory&cache=shared"
	_ = dbcore.GetDBInstance()

	taskID := "rpc-task-filter"
	require.NoError(t, tasks.CreateTask(taskID, []string{"node-a", "node-b"}, "true"))
	ctx := adminTaskTestContext()

	res, rpcErr := adminGetTaskResultsByTaskId(ctx, &rpc.JsonRpcRequest{
		Params: map[string]any{"task_id": taskID, "clients": "missing-node"},
	})
	require.Nil(t, rpcErr, "filtered empty results must not be 404")
	rows, ok := res.([]models.TaskResult)
	require.True(t, ok)
	require.Len(t, rows, 0)

	filtered, rpcErr := adminGetTaskResultsByTaskId(ctx, &rpc.JsonRpcRequest{
		Params: map[string]any{"task_id": taskID, "clients": "node-b"},
	})
	require.Nil(t, rpcErr)
	filteredRows, ok := filtered.([]models.TaskResult)
	require.True(t, ok)
	require.Len(t, filteredRows, 1)
	require.Equal(t, "node-b", filteredRows[0].Client)

	_, rpcErr = adminGetTaskResultsByTaskId(ctx, &rpc.JsonRpcRequest{
		Params: map[string]any{"task_id": "does-not-exist"},
	})
	require.NotNil(t, rpcErr)
	require.Equal(t, rpc.NotFound, rpcErr.Code)
}
