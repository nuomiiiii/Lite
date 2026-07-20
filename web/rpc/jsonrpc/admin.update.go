package jsonrpc

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/pkg/metric"
	"github.com/komari-monitor/komari/pkg/rpc"
	"github.com/komari-monitor/komari/pkg/selfupdate"
)

func init() {
	RegisterWithGroupAndMeta("getSelfUpdateStatus", rpc.RoleAdmin, adminGetSelfUpdateStatus, &rpc.MethodMeta{
		Name:    "admin:getSelfUpdateStatus",
		Summary: "Return Linux self-update capability and the latest transaction result",
		Returns: "SelfUpdateCapability",
	})
	RegisterWithGroupAndMeta("startSelfUpdate", rpc.RoleAdmin, adminStartSelfUpdate, &rpc.MethodMeta{
		Name:    "admin:startSelfUpdate",
		Summary: "Download, verify and schedule an atomic Linux update",
		Params: []rpc.ParamMeta{
			{Name: "version", Type: "string", Required: true},
			{Name: "version_hash", Type: "string", Required: true},
		},
		Returns: "SelfUpdateResult",
	})
}

func adminGetSelfUpdateStatus(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	capability := resolveSelfUpdateCapability()
	return capability, nil
}

func adminStartSelfUpdate(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Version     string `json:"version"`
		VersionHash string `json:"version_hash"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid update request: "+err.Error(), nil)
	}
	capability := resolveSelfUpdateCapability()
	if !capability.Supported {
		return nil, rpc.MakeError(rpc.InvalidParams, "Self update is unavailable: "+capability.Reason, capability)
	}

	updateCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	result, err := selfupdate.PrepareAndLaunch(updateCtx, params.Version, params.VersionHash)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to prepare update: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "scheduled self update to "+params.Version+" ("+params.VersionHash+")", "warn")
	return result, nil
}

func resolveSelfUpdateCapability() selfupdate.Capability {
	capability := selfupdate.DetectCapability()
	if !capability.Supported {
		return capability
	}
	settings, err := config.GetManyAs[metricstore.MetricStoreConfig]()
	if err != nil {
		capability.Supported = false
		capability.Reason = "metric_configuration_unavailable"
		return capability
	}
	if metricstore.ResolveDriverFromConfig(settings.Driver, settings.DSN) != metric.DriverSQLite {
		capability.Supported = false
		capability.Reason = "external_metric_database"
		return capability
	}
	workingDir, err := os.Getwd()
	if err != nil || !selfupdate.PathWithinData(settings.DSN, filepath.Join(workingDir, "data"), workingDir) {
		capability.Supported = false
		capability.Reason = "metrics_database_outside_data_directory"
	}
	return capability
}
