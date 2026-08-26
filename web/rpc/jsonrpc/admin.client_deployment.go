package jsonrpc

import (
	"context"
	"errors"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/pkg/rpc"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
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
	profile, saved, deliveryState, err := clients.GetDeploymentProfileWithDelivery(params.UUID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, rpc.MakeError(rpc.InvalidParams, "Client not found", nil)
		}
		return nil, rpc.MakeError(rpc.InternalError, "Failed to load deployment profile: "+err.Error(), nil)
	}
	return map[string]any{"profile": profile, "saved": saved, "delivery_state": deliveryState}, nil
}

func adminSaveClientDeploymentProfile(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID    string                    `json:"uuid"`
		Profile clients.DeploymentProfile `json:"profile"`
	}
	if err := req.BindParams(&params); err != nil || params.UUID == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid deployment profile", nil)
	}
	profile, deliveryState, runtimeChanged, err := clients.SaveDeploymentProfileForDispatch(params.UUID, params.Profile)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, rpc.MakeError(rpc.InvalidParams, "Client not found", nil)
		}
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}

	delivery := "saved"
	if runtimeChanged {
		runtimeConfig := profile.RuntimeConfig()
		runtimeConfig.Revision = deliveryState.Revision
		_, sent, supported := agent_runtime.DispatchV2Config(params.UUID, runtimeConfig)
		if sent {
			delivery = "sent"
			if _, markErr := clients.MarkDeploymentConfigSent(params.UUID, deliveryState.Revision); markErr != nil {
				return nil, rpc.MakeError(rpc.InternalError, "Failed to update deployment delivery state: "+markErr.Error(), nil)
			}
			deliveryState.Status = clients.DeploymentDeliverySent
		} else if !supported && agent_runtime.IsAgentOnline(params.UUID) {
			delivery = "agent_upgrade_required"
		}
	} else {
		delivery = deliveryState.Status
	}

	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "save client deployment profile:"+params.UUID, "info")
	return map[string]any{
		"profile":         profile,
		"delivery":        delivery,
		"delivery_state":  deliveryState,
		"runtime_changed": runtimeChanged,
	}, nil
}
