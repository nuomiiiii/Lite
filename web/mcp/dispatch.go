package mcp

import (
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api/remote"
)

func dispatchMCPExec(uuid string, params v2.MCPExecParams) bool {
	queued := false
	accepted := agent_runtime.GuardRemoteDelivery(func() bool {
		return remote.RemoteManagementEnabled() && mcpEnabled()
	}, func() {
		event := agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentMCPExec, params)
		queued = event.ID != ""
		if queued {
			if conn := agent_runtime.GetConnectedClient(uuid); conn != nil {
				payload := v2.Request{JSONRPC: v2.Version, Method: event.Method, Params: event.Params, ID: event.ID}
				_ = conn.WriteJSON(payload)
			}
		}
	})
	return accepted && queued
}

func dispatchMCPFile(uuid string, params v2.MCPFileParams) bool {
	queued := false
	accepted := agent_runtime.GuardRemoteDelivery(func() bool {
		return remote.RemoteManagementEnabled() && mcpEnabled()
	}, func() {
		event := agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentMCPFile, params)
		queued = event.ID != ""
		if queued {
			if conn := agent_runtime.GetConnectedClient(uuid); conn != nil {
				payload := v2.Request{JSONRPC: v2.Version, Method: event.Method, Params: event.Params, ID: event.ID}
				_ = conn.WriteJSON(payload)
			}
		}
	})
	return accepted && queued
}

func mcpMethods() []string {
	return []string{
		v2.MethodAgentMCPExec,
		v2.MethodAgentMCPCancel,
		v2.MethodAgentMCPRenew,
		v2.MethodAgentMCPRevoke,
		v2.MethodAgentMCPFile,
	}
}

func DrainMCPDelivery() []agent_runtime.RemovedV2Event {
	return agent_runtime.DrainRemoteDelivery(func() []agent_runtime.RemovedV2Event {
		_ = RevokeAllActive(reasonMCPOff)
		return agent_runtime.RemoveAllV2EventsByMethods(mcpMethods()...)
	})
}
