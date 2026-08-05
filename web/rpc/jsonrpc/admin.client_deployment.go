package jsonrpc

import (
	"context"
	"errors"

	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/pkg/rpc"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
	"gorm.io/gorm"
)

func init() {
	RegisterWithGroupAndMeta("getClientDeploymentProfile", rpc.RoleAdmin, adminGetClientDeploymentProfile, &rpc.MethodMeta{
		Name:    "admin:getClientDeploymentProfile",
		Summary: "Get a client's saved deployment profile",
		Params: []rpc.ParamMeta{
			{Name: "uuid", Type: "string", Required: true, Description: "Client UUID"},
		},
		Returns: "{ profile: DeploymentProfile, saved: boolean }",
	})
	RegisterWithGroupAndMeta("saveClientDeploymentProfile", rpc.RoleAdmin, adminSaveClientDeploymentProfile, &rpc.MethodMeta{
		Name:    "admin:saveClientDeploymentProfile",
		Summary: "Save a client's deployment profile and dispatch runtime-safe settings",
		Params: []rpc.ParamMeta{
			{Name: "uuid", Type: "string", Required: true, Description: "Client UUID"},
			{Name: "profile", Type: "DeploymentProfile", Required: true},
		},
		Returns: "{ profile: DeploymentProfile, delivery: string }",
	})
}

func adminGetClientDeploymentProfile(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID string `json:"uuid"`
	}
	if err := req.BindParams(&params); err != nil || params.UUID == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid or missing UUID", nil)
	}
	profile, saved, err := clients.GetDeploymentProfile(params.UUID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, rpc.MakeError(rpc.InvalidParams, "Client not found", nil)
		}
		return nil, rpc.MakeError(rpc.InternalError, "Failed to load deployment profile: "+err.Error(), nil)
	}
	return map[string]any{"profile": profile, "saved": saved}, nil
}

func adminSaveClientDeploymentProfile(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID    string                    `json:"uuid"`
		Profile clients.DeploymentProfile `json:"profile"`
	}
	if err := req.BindParams(&params); err != nil || params.UUID == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid deployment profile", nil)
	}
	profile, err := clients.SaveDeploymentProfile(params.UUID, params.Profile)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, rpc.MakeError(rpc.InvalidParams, "Client not found", nil)
		}
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}

	runtimeConfig := profile.RuntimeConfig()
	delivery := "saved_for_reconnect"
	if agent_runtime.DispatchV2Event(params.UUID, v2.MethodAgentConfig, runtimeConfig) {
		delivery = "dispatched"
	} else if agent_runtime.IsAgentOnline(params.UUID) {
		delivery = "agent_upgrade_required"
	}

	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "save client deployment profile:"+params.UUID, "info")
	return map[string]any{
		"profile":  profile,
		"delivery": delivery,
	}, nil
}
