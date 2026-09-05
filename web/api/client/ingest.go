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

// ingestPingResult 保存一条 ping 探测结果。
func ingestPingResult(uuid string, taskID uint, value int) error {
	return tasks.SavePingRecord(models.PingRecord{
		Client: uuid,
		TaskId: taskID,
		Value:  value,
		Time:   time.Now().UTC(),
	})
}

func ingestTaskResult(uuid string, params v2.TaskResultParams) error {
	finishedAt := params.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	return tasks.SaveIncomingTaskResult(params.TaskID, uuid, params.Result, params.Status, params.ExitCode, finishedAt)
}
