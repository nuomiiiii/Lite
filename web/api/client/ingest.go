package client

import (
	"context"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/mcp"
)

// ingest.go
// agent 上报数据的传输无关处理逻辑。JSON-RPC 入口解析后统一调用这里落库并更新运行时状态。

// ingestReport 保存一次负载上报并刷新运行时状态。
// 节点身份只能来自已认证的 Token（调用方传入的 uuid），不能信任上报正文中的 UUID。
// markPresence 为 true 时按 POST 上报会话刷新在线状态（WS 连接自行管理在线状态，应传 false）。
func ingestReport(uuid string, report v2.Report, markPresence bool) error {
	report.UUID = uuid
	report.UpdatedAt = time.Now().UTC()
	if err := clients.ReportVerify(report); err != nil {
		return err
	}
	savedReport, err := metricstore.WriteReport(context.Background(), report)
	if err != nil {
		return err
	}
	agent_runtime.RecordReport(savedReport)
	if markPresence {
		refreshPostPresence(uuid)
	}
	return nil
}

// ingestBasicInfo 保存客户端基础信息。fallbackIP 在上报未携带 IP 时用作兜底。
func ingestBasicInfo(uuid string, info map[string]interface{}, fallbackIP string) error {
	if info == nil {
		info = map[string]interface{}{}
	}
	return saveClientBasicInfo(info, uuid, fallbackIP)
}

func ingestMCPCapability(uuid string, params v2.PullParams) {
	full := false
	for _, capability := range params.Capabilities {
		if capability == v2.CapabilityMCPFull {
			full = true
			break
		}
	}
	version := 0
	if full && params.CapabilityVersions != nil {
		version = params.CapabilityVersions[v2.CapabilityMCPFull]
	}
	clients.SetMCPCapability(uuid, full, version)
}

const pingResultLookback = 6 * time.Hour

// ingestPingResult 保存一条 ping 探测结果。
func ingestPingResult(uuid string, taskID uint, value float64, finishedAt time.Time) error {
	return tasks.SavePingRecord(models.PingRecord{
		Client: uuid,
		TaskId: taskID,
		Value:  value,
		Time:   pingResultTime(finishedAt),
	})
}

func pingResultTime(finishedAt time.Time) time.Time {
	return pingResultTimeAt(finishedAt, time.Now().UTC())
}

func pingResultTimeAt(finishedAt, now time.Time) time.Time {
	now = now.UTC()
	if finishedAt.IsZero() {
		return now
	}
	candidate := finishedAt.UTC()
	if candidate.After(now.Add(time.Minute)) || now.Sub(candidate) > pingResultLookback {
		return now
	}
	return candidate
}

func ingestTaskResult(uuid string, params v2.TaskResultParams) error {
	finishedAt := params.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	completed := mcp.CompleteOperation(params.TaskID, uuid, params)
	err := tasks.SaveIncomingTaskResult(params.TaskID, uuid, params.Result, params.Status, params.ExitCode, finishedAt)
	if err == nil || completed {
		// Drop the queued copy. Leaving it until the event TTL lets a later
		// expiry write "delivery timeout" over the output the node just sent.
		agent_runtime.RemoveV2EventsByTaskID(uuid, params.TaskID)
		return nil
	}
	return err
}
