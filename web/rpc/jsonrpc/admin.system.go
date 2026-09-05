package jsonrpc

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"

	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils"
	"github.com/nuomiiiii/lite/utils/cloudflared"
	"github.com/nuomiiiii/lite/utils/geoip"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/utils/messageSender"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api/remote"
	"github.com/nuomiiiii/lite/web/remotectl"
	"gorm.io/gorm"
)

// admin.system.go
// 系统/运维类 RPC2 方法（admin 命名空间）：日志、Cloudflare Tunnel、远程执行、测试。

const cloudflaredStopConfirmText = "STOP CLOUDFLARED"

func init() {
	RegisterWithGroupAndMeta("getLogs", rpc.RoleAdmin, adminGetLogs, &rpc.MethodMeta{
		Name:    "admin:getLogs",
		Summary: "Get audit logs (paged, optionally filtered by message type)",
		Params: []rpc.ParamMeta{
			{Name: "limit", Type: "string", Description: "Page size (default 100)"},
			{Name: "page", Type: "string", Description: "One-based page number (default 1)"},
			{Name: "msg_type", Type: "string", Description: "Optional exact message type filter"},
		},
		Returns: "{ logs: Log[], total: number }",
	})
	reg("getCloudflaredStatus", adminCloudflaredStatus, "Get cloudflared tunnel status")
	reg("startCloudflared", adminStartCloudflared, "Start cloudflared tunnel")
	reg("stopCloudflared", adminStopCloudflared, "Stop cloudflared tunnel")
	reg("removeCloudflaredToken", adminRemoveCloudflaredToken, "Remove cloudflared tunnel token")
	reg("exec", adminExec, "Execute a command on clients")

	reg("testSendMessage", adminTestSendMessage, "Send a test notification")
	reg("testGeoip", adminTestGeoip, "Test GeoIP lookup")

	agent_runtime.SetExpiredV2ExecHandler(func(uuid, taskID string) {
		if err := persistIncomingTaskResult(taskID, uuid, v2.DeliveryTimeoutTaskResult, "", -1, time.Now().UTC()); err != nil {
			logger.Errorf("rpc", "failed to persist exec delivery timeout for task %s client %s: %v", taskID, uuid, err)
		}
	})
}

func adminGetLogs(_ context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Limit   string `json:"limit"`
		Page    string `json:"page"`
		MsgType string `json:"msg_type"`
	}
	req.BindParams(&params)
	if params.Limit == "" {
		params.Limit = "100"
	}
	if params.Page == "" {
		params.Page = "1"
	}
	limitInt, err := strconv.Atoi(params.Limit)
	if err != nil || limitInt <= 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid limit: "+params.Limit, nil)
	}
	pageInt, err := strconv.Atoi(params.Page)
	if err != nil || pageInt <= 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid page: "+params.Page, nil)
	}
	db := dbcore.GetDBInstance()
	logs, total, err := queryAdminLogs(db, limitInt, pageInt, params.MsgType)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to retrieve logs: "+err.Error(), nil)
	}
	return map[string]any{"logs": logs, "total": total}, nil
}

func queryAdminLogs(db *gorm.DB, limit, page int, msgType string) ([]models.Log, int64, error) {
	var logs []models.Log
	var total int64
	offset := (page - 1) * limit
	countQuery := filterAdminLogsByMessageType(db.Model(&models.Log{}), msgType)
	logsQuery := filterAdminLogsByMessageType(db.Model(&models.Log{}), msgType)
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := logsQuery.Order("time desc").Limit(limit).Offset(offset).Find(&logs).Error; err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

func filterAdminLogsByMessageType(query *gorm.DB, msgType string) *gorm.DB {
	if msgType = strings.TrimSpace(msgType); msgType != "" {
		return query.Where("msg_type = ?", msgType)
	}
	return query
}

func adminCloudflaredStatus(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return cloudflared.Status(), nil
}

func adminStartCloudflared(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		Token string `json:"token"`
	}
	req.BindParams(&params)
	token := strings.TrimSpace(params.Token)
	if token != "" {
		if err := cloudflared.SaveToken(token); err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "Failed to save Cloudflare Tunnel token: "+err.Error(), nil)
		}
	}
	if err := cloudflared.Start(token); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "started cloudflared tunnel", "warn")
	return cloudflared.Status(), nil
}

func adminStopCloudflared(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		CurrentPassword string `json:"current_password"`
		ConfirmText     string `json:"confirm_text"`
	}
	req.BindParams(&params)

	disablePasswordLogin, _ := config.GetAs[bool](config.DisablePasswordLoginKey, false)
	if !disablePasswordLogin {
		actor, _ := auditActor(ctx)
		if actor == "" {
			return nil, rpc.MakeError(rpc.Unauthenticated, "Unauthorized.", nil)
		}
		user, err := accounts.GetUserByUUID(actor)
		if err != nil {
			return nil, rpc.MakeError(rpc.Unauthenticated, "Failed to verify current user", nil)
		}
		if strings.TrimSpace(params.CurrentPassword) == "" {
			return nil, rpc.MakeError(rpc.InvalidParams, "Current password is required", nil)
		}
		if _, ok := accounts.CheckPassword(user.Username, params.CurrentPassword); !ok {
			return nil, rpc.MakeError(rpc.Unauthenticated, "Current password is incorrect", nil)
		}
	} else if strings.TrimSpace(params.ConfirmText) != cloudflaredStopConfirmText {
		return nil, rpc.MakeError(rpc.InvalidParams, "Type STOP CLOUDFLARED to confirm stopping cloudflared", nil)
	}

	if err := cloudflared.Stop(); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to stop cloudflared: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "stopped cloudflared tunnel", "warn")
	return cloudflared.Status(), nil
}

func adminRemoveCloudflaredToken(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if err := cloudflared.RemoveToken(); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Failed to remove Cloudflare Tunnel token: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "removed cloudflared tunnel token", "warn")
	return cloudflared.Status(), nil
}

func adminExec(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	meta := rpc.MetaFromContext(ctx)
	if meta == nil || meta.Principal == nil || meta.Principal.Type != rpc.PrincipalUser || meta.SessionToken == "" {
		return nil, rpc.MakeError(rpc.PermissionDenied, "Remote execution requires an administrator session", nil)
	}
	if meta.Principal.IsAPIKey || meta.Principal.Type == rpc.PrincipalAPIKey {
		return nil, rpc.MakeError(rpc.PermissionDenied, remotectl.ErrAPIKeyForbidden.Error(), nil)
	}
	if !remote.RemoteManagementEnabled() {
		return nil, rpc.MakeError(rpc.PermissionDenied, "站点未启用远程管理", nil)
	}

	var params struct {
		Command string   `json:"command"`
		Clients []string `json:"clients"`
		Grant   string   `json:"grant"`
		PageID  string   `json:"page_id"`
	}
	req.BindParams(&params)
	command := params.Command
	if strings.TrimSpace(command) == "" {
		return nil, rpc.MakeError(rpc.InvalidParams, "Command cannot be empty", nil)
	}
	if len(command) > maxExecCommandBytes {
		return nil, rpc.MakeError(rpc.InvalidParams, "Command is too long", nil)
	}
	if len(params.Clients) == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "clients is required", nil)
	}
	expires, err := remotectl.TakeExecGrant(params.Grant, meta.Principal.UserUUID, meta.SessionToken, params.PageID)
	if err != nil {
		if remotectl.IsRateLimited(err) {
			return nil, rpc.MakeError(rpc.PermissionDenied, err.Error(), nil)
		}
		return nil, rpc.MakeError(rpc.PermissionDenied, err.Error(), nil)
	}

	uuids := uniqueUUIDs(params.Clients)
	known, err := clients.GetClientsByUUIDs(uuids)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to load clients: "+err.Error(), nil)
	}
	classified := classifyRemoteExecTargets(uuids, known, func(uuid string) bool {
		return agent_runtime.GetConnectedClient(uuid) != nil
	}, agent_runtime.IsAgentOnline)
	if len(classified.live) == 0 && len(classified.queued) == 0 {
		return nil, rpc.MakeError(rpc.InvalidParams, "No clients connected", nil)
	}
	taskId := utils.GenerateRandomString(16)
	taskClients := make([]string, 0, len(classified.live)+len(classified.queued)+len(classified.offline)+len(classified.unavailable))
	taskClients = append(taskClients, classified.live...)
	taskClients = append(taskClients, classified.queued...)
	taskClients = append(taskClients, classified.offline...)
	taskClients = append(taskClients, classified.unavailable...)
	if err := tasks.CreateTask(taskId, taskClients, command); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to create task: "+err.Error(), nil)
	}

	payload := v2.ExecParams{TaskID: taskId, Command: command}
	var actuallySent, actuallyQueued, deliveryFailed []string
	delivered := agent_runtime.GuardRemoteDelivery(remote.RemoteManagementEnabled, func() {
		for _, uuid := range classified.live {
			queued, notified := agent_runtime.DispatchV2ExecEvent(uuid, payload)
			if !queued {
				deliveryFailed = append(deliveryFailed, uuid)
				continue
			}
			if notified {
				actuallySent = append(actuallySent, uuid)
				continue
			}
			actuallyQueued = append(actuallyQueued, uuid)
		}
		for _, uuid := range classified.queued {
			queued, notified := agent_runtime.DispatchV2ExecEvent(uuid, payload)
			if !queued {
				deliveryFailed = append(deliveryFailed, uuid)
				continue
			}
			if notified {
				actuallySent = append(actuallySent, uuid)
				continue
			}
			actuallyQueued = append(actuallyQueued, uuid)
		}
	})
	if !delivered {
		if err := cancelExecTaskClients(taskId, taskClients); err != nil {
			return nil, rpc.MakeError(rpc.InternalError, "远程管理已关闭，但未能写入已取消任务结果: "+err.Error(), nil)
		}
		return nil, rpc.MakeError(rpc.PermissionDenied, "站点未启用远程管理", nil)
	}
	finishedAt := time.Now().UTC()
	result := map[string]any{
		"task_id":         taskId,
		"clients":         actuallySent,
		"queued_clients":  actuallyQueued,
		"offline_clients": classified.offline,
		"failed_clients":  uniqueStrings(append(append([]string{}, classified.unavailable...), deliveryFailed...), actuallySent, actuallyQueued),
	}
	if err := persistExecTerminalResults(taskId, classified.offline, deliveryFailed, classified.unavailable, finishedAt); err != nil {
		logger.Errorf("rpc", "failed to persist terminal exec results for task %s: %v", taskId, err)
		result["persist_error"] = err.Error()
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "REC, task id: "+taskId, "warn")
	if nextGrant, nextExpires, rotateErr := remotectl.RotateExecGrant(meta.Principal.UserUUID, meta.SessionToken, params.PageID, expires); rotateErr == nil {
		result["next_grant"] = nextGrant
		result["expires_at"] = nextExpires.UTC()
	}
	return result, nil
}

var persistTaskResults = tasks.SaveTaskResults
var persistIncomingTaskResult = tasks.SaveIncomingTaskResult

func persistExecTerminalResults(taskId string, offline, deliveryFailed, unavailable []string, finishedAt time.Time) error {
	var first error
	save := func(ids []string, result string) {
		if err := persistTaskResults(taskId, ids, result, -1, finishedAt); err != nil && first == nil {
			first = err
		}
	}
	save(offline, "Client offline!")
	save(deliveryFailed, "delivery failed")
	save(unavailable, "remote control unavailable")
	return first
}

const maxExecCommandBytes = 64 << 10

type remoteExecTargets struct {
	live        []string
	queued      []string
	offline     []string
	unavailable []string
}

func uniqueUUIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func classifyRemoteExecTargets(uuids []string, known map[string]models.Client, connected, online func(string) bool) remoteExecTargets {
	var classified remoteExecTargets
	for _, uuid := range uuids {
		client, ok := known[uuid]
		if !ok {
			classified.unavailable = append(classified.unavailable, uuid)
			continue
		}
		if err := remote.AgentRemoteAllowed(client); err != nil {
			classified.unavailable = append(classified.unavailable, uuid)
			continue
		}
		if connected(uuid) {
			classified.live = append(classified.live, uuid)
			continue
		}
		if online(uuid) {
			classified.queued = append(classified.queued, uuid)
			continue
		}
		classified.offline = append(classified.offline, uuid)
	}
	return classified
}

func uniqueStrings(values []string, exclude ...[]string) []string {
	skip := make(map[string]struct{})
	for _, group := range exclude {
		for _, value := range group {
			skip[value] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := skip[value]; ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func adminTestSendMessage(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	err := messageSender.SendEvent(models.EventMessage{
		Event:   "Test",
		Time:    time.Now().UTC(),
		Message: "This is a test message from Lite.",
	})
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to send message: "+err.Error(), nil)
	}
	return nil, nil
}

func adminTestGeoip(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		IP string `json:"ip"`
	}
	req.BindParams(&params)
	ip := params.IP
	if ip == "" {
		if meta := rpc.MetaFromContext(ctx); meta != nil {
			ip = meta.RemoteIP
		}
	}
	cfg, err := config.GetAs[bool](config.GeoIpEnabledKey, false)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get configuration: "+err.Error(), nil)
	}
	if !cfg {
		return nil, rpc.MakeError(rpc.InvalidParams, "GeoIP is not enabled in the configuration.", nil)
	}
	record, err := geoip.GetGeoInfo(net.ParseIP(ip))
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to get GeoIP record: "+err.Error(), nil)
	}
	return record, nil
}
