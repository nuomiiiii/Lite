package jsonrpc

import (
	"context"
	"encoding/json"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/utils"
)

func init() {
	RegisterWithGroupAndMeta("getReturnRouteOverview", rpc.RoleAdmin, adminGetReturnRouteOverview, &rpc.MethodMeta{Name: "admin:getReturnRouteOverview", Summary: "List return route tasks, current states and events"})
	RegisterWithGroupAndMeta("getReturnRouteSummary", rpc.RoleAdmin, adminGetReturnRouteSummary, &rpc.MethodMeta{Name: "admin:getReturnRouteSummary", Summary: "Get return route monitoring counters"})
	RegisterWithGroupAndMeta("queryReturnRouteTasks", rpc.RoleAdmin, adminQueryReturnRouteTasks, &rpc.MethodMeta{Name: "admin:queryReturnRouteTasks", Summary: "Query return route tasks and current states with filters and pagination"})
	RegisterWithGroupAndMeta("queryReturnRouteEvents", rpc.RoleAdmin, adminQueryReturnRouteEvents, &rpc.MethodMeta{Name: "admin:queryReturnRouteEvents", Summary: "Query return route events with filters and pagination"})
	RegisterWithGroupAndMeta("addReturnRouteTask", rpc.RoleAdmin, adminAddReturnRouteTask, &rpc.MethodMeta{Name: "admin:addReturnRouteTask", Summary: "Create a return route task"})
	RegisterWithGroupAndMeta("editReturnRouteTask", rpc.RoleAdmin, adminEditReturnRouteTask, &rpc.MethodMeta{Name: "admin:editReturnRouteTask", Summary: "Edit a return route task"})
	RegisterWithGroupAndMeta("batchEditReturnRouteTasks", rpc.RoleAdmin, adminBatchEditReturnRouteTasks, &rpc.MethodMeta{Name: "admin:batchEditReturnRouteTasks", Summary: "Edit multiple return route tasks"})
	RegisterWithGroupAndMeta("deleteReturnRouteTask", rpc.RoleAdmin, adminDeleteReturnRouteTask, &rpc.MethodMeta{Name: "admin:deleteReturnRouteTask", Summary: "Delete return route tasks"})
	RegisterWithGroupAndMeta("probeReturnRouteNow", rpc.RoleAdmin, adminProbeReturnRouteNow, &rpc.MethodMeta{Name: "admin:probeReturnRouteNow", Summary: "Dispatch a return route probe immediately"})
	RegisterWithGroupAndMeta("getReturnRouteRules", rpc.RoleAdmin, adminGetReturnRouteRules, &rpc.MethodMeta{Name: "admin:getReturnRouteRules", Summary: "Get the active return route signature rules and reload status"})
	RegisterWithGroupAndMeta("reloadReturnRouteRules", rpc.RoleAdmin, adminReloadReturnRouteRules, &rpc.MethodMeta{Name: "admin:reloadReturnRouteRules", Summary: "Reload return route signature rules from disk"})
	RegisterWithGroupAndMeta("updateReturnRouteRules", rpc.RoleAdmin, adminUpdateReturnRouteRules, &rpc.MethodMeta{Name: "admin:updateReturnRouteRules", Summary: "Validate, store and activate return route signature rules"})
	RegisterWithGroupAndMeta("refreshReturnRouteBGPRules", rpc.RoleAdmin, adminRefreshReturnRouteBGPRules, &rpc.MethodMeta{Name: "admin:refreshReturnRouteBGPRules", Summary: "Download and activate the latest filtered BGP prefix rules"})
	RegisterWithGroupAndMeta("listReturnRouteTemplates", rpc.RoleAdmin, adminListReturnRouteTemplates, &rpc.MethodMeta{Name: "admin:listReturnRouteTemplates", Summary: "List return route templates"})
	RegisterWithGroupAndMeta("addReturnRouteTemplate", rpc.RoleAdmin, adminAddReturnRouteTemplate, &rpc.MethodMeta{Name: "admin:addReturnRouteTemplate", Summary: "Create a return route template"})
	RegisterWithGroupAndMeta("editReturnRouteTemplate", rpc.RoleAdmin, adminEditReturnRouteTemplate, &rpc.MethodMeta{Name: "admin:editReturnRouteTemplate", Summary: "Edit a return route template"})
	RegisterWithGroupAndMeta("batchEditReturnRouteTemplates", rpc.RoleAdmin, adminBatchEditReturnRouteTemplates, &rpc.MethodMeta{Name: "admin:batchEditReturnRouteTemplates", Summary: "Edit multiple return route templates"})
	RegisterWithGroupAndMeta("deleteReturnRouteTemplates", rpc.RoleAdmin, adminDeleteReturnRouteTemplates, &rpc.MethodMeta{Name: "admin:deleteReturnRouteTemplates", Summary: "Delete return route templates"})
	RegisterWithGroupAndMeta("applyReturnRouteTemplates", rpc.RoleAdmin, adminApplyReturnRouteTemplates, &rpc.MethodMeta{Name: "admin:applyReturnRouteTemplates", Summary: "Create return route tasks from templates for selected servers"})
}

func adminGetReturnRouteSummary(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	result, err := tasks.GetReturnRouteSummary()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	return result, nil
}

func adminQueryReturnRouteTasks(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params tasks.ReturnRouteTaskQuery
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	result, err := tasks.QueryReturnRouteTasks(params)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return result, nil
}

func adminQueryReturnRouteEvents(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params tasks.ReturnRouteEventQuery
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	result, err := tasks.QueryReturnRouteEvents(params)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return result, nil
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

func adminBatchEditReturnRouteTasks(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params tasks.ReturnRouteTaskBatchEdit
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if err := tasks.EditReturnRouteTasksBatch(params); err != nil {
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

func adminGetReturnRouteRules(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return tasks.GetReturnRouteRules(), nil
}

func adminReloadReturnRouteRules(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	result, err := tasks.ReloadReturnRouteRules()
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), result)
	}
	return result, nil
}

func adminUpdateReturnRouteRules(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Rules json.RawMessage `json:"rules"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if len(params.Rules) == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "rules are required", nil)
	}
	result, err := tasks.UpdateReturnRouteRules(params.Rules)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), result)
	}
	return result, nil
}

func adminListReturnRouteTemplates(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	templates, err := tasks.ListReturnRouteTemplates()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	return templates, nil
}

func adminAddReturnRouteTemplate(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var template models.ReturnRouteTemplate
	if err := req.BindParams(&template); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	id, err := tasks.AddReturnRouteTemplate(&template)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return map[string]any{"template_id": id}, nil
}

func adminEditReturnRouteTemplate(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var template models.ReturnRouteTemplate
	if err := req.BindParams(&template); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if err := tasks.EditReturnRouteTemplate(&template); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return nil, nil
}

func adminBatchEditReturnRouteTemplates(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params tasks.ReturnRouteTemplateBatchEdit
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	if err := tasks.EditReturnRouteTemplatesBatch(params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return nil, nil
}

func adminDeleteReturnRouteTemplates(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		IDs []uint `json:"ids"`
	}
	if err := req.BindParams(&params); err != nil || len(params.IDs) == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "ids are required", nil)
	}
	if err := tasks.DeleteReturnRouteTemplates(params.IDs); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	return nil, nil
}

func adminApplyReturnRouteTemplates(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		TemplateIDs []uint   `json:"template_ids"`
		Clients     []string `json:"clients"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	result, err := tasks.ApplyReturnRouteTemplates(params.TemplateIDs, params.Clients)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	return result, nil
}

func adminRefreshReturnRouteBGPRules(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	result, err := tasks.RefreshReturnRouteBGPRules(ctx)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), result)
	}
	return result, nil
}
