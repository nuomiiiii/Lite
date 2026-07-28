package jsonrpc

import (
	"context"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/pkg/rpc"
	"github.com/komari-monitor/komari/utils"
)

func init() {
	RegisterWithGroupAndMeta("getReturnRouteOverview", rpc.RoleAdmin, adminGetReturnRouteOverview, &rpc.MethodMeta{Name: "admin:getReturnRouteOverview", Summary: "List return route tasks, current states and events"})
	RegisterWithGroupAndMeta("addReturnRouteTask", rpc.RoleAdmin, adminAddReturnRouteTask, &rpc.MethodMeta{Name: "admin:addReturnRouteTask", Summary: "Create a return route task"})
	RegisterWithGroupAndMeta("editReturnRouteTask", rpc.RoleAdmin, adminEditReturnRouteTask, &rpc.MethodMeta{Name: "admin:editReturnRouteTask", Summary: "Edit a return route task"})
	RegisterWithGroupAndMeta("deleteReturnRouteTask", rpc.RoleAdmin, adminDeleteReturnRouteTask, &rpc.MethodMeta{Name: "admin:deleteReturnRouteTask", Summary: "Delete return route tasks"})
	RegisterWithGroupAndMeta("probeReturnRouteNow", rpc.RoleAdmin, adminProbeReturnRouteNow, &rpc.MethodMeta{Name: "admin:probeReturnRouteNow", Summary: "Dispatch a return route probe immediately"})
}

func adminGetReturnRouteOverview(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	result, err := tasks.GetReturnRouteOverview()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	return result, nil
}

func adminAddReturnRouteTask(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var task models.ReturnRouteTask
	if err := req.BindParams(&task); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	id, dispatched, err := tasks.AddReturnRouteTask(&task)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return map[string]any{"task_id": id, "dispatched": dispatched}, nil
}

func adminEditReturnRouteTask(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var task models.ReturnRouteTask
	if err := req.BindParams(&task); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if err := tasks.EditReturnRouteTask(&task); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return nil, nil
}

func adminDeleteReturnRouteTask(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		IDs []uint `json:"ids"`
	}
	if err := req.BindParams(&params); err != nil || len(params.IDs) == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "ids are required", nil)
	}
	if err := tasks.DeleteReturnRouteTasks(params.IDs); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	return nil, nil
}

func adminProbeReturnRouteNow(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		ID uint `json:"id"`
	}
	if err := req.BindParams(&params); err != nil || params.ID == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "id is required", nil)
	}
	var task models.ReturnRouteTask
	if err := dbcore.GetDBInstance().First(&task, params.ID).Error; err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if !task.Enabled {
		return nil, rpc.MakeError(rpc.InvalidParams, "task is disabled", nil)
	}
	if !utils.DispatchReturnRouteTask(task) {
		return nil, rpc.MakeError(rpc.InternalError, "agent is offline or does not support route probes", nil)
	}
	return map[string]any{"dispatched": true}, nil
}
