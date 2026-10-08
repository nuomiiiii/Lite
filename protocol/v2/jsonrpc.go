package v2

import (
	"encoding/json"
	"time"
)

const (
	Version                 = "2.0"
	MethodAgentReport       = "agent.report"
	MethodAgentBasicInfo    = "agent.basicInfo"
	MethodAgentPingResult   = "agent.pingResult"
	MethodAgentRouteResult  = "agent.routeResult"
	MethodAgentTaskResult   = "agent.taskResult"
	MethodAgentExec         = "agent.exec"
	MethodAgentMCPExec      = "agent.mcp.exec"
	MethodAgentMCPCancel    = "agent.mcp.cancel"
	MethodAgentMCPRenew     = "agent.mcp.renew"
	MethodAgentMCPRevoke    = "agent.mcp.revoke"
	MethodAgentMCPFile      = "agent.mcp.file"
	MethodAgentPing         = "agent.ping"
	MethodAgentRoute        = "agent.route"
	MethodAgentMessage      = "agent.message"
	MethodAgentEvent        = "agent.event"
	MethodAgentRemote       = "agent.remote.request"
	MethodAgentConfig       = "agent.config"
	MethodAgentConfigResult = "agent.configResult"
	MethodAgentPull         = "agent.pull"

	CapabilityMCPFull = "mcp_full"
	MCPFullVersion    = 1
)

type Request struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	ID      any    `json:"id,omitempty"`
}

type Response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type Event struct {
	ID        string    `json:"id"`
	Method    string    `json:"method"`
	Params    any       `json:"params,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// HandedOff is set once a websocket send succeeds or an agent pull copies
	// the event. It stays on the queued copy until ACK. Lack of an ACK does
	// not mean the agent never received the command.
	HandedOff bool `json:"-"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type ReportParams struct {
	Report      Report   `json:"report"`
	AckEventIDs []string `json:"ack_event_ids,omitempty"`
}

const (
	TaskResultStatusFinished         = "finished"
	TaskResultStatusInterrupted      = "interrupted"
	InterruptedTaskResultText        = "execution status unknown"
	DeliveryTimeoutTaskResult        = "delivery timeout"
	RemoteManagementClosedTaskResult = "远程管理已关闭，任务未投递/已取消"
	// ScheduledExecStoppedTaskResult is stored when a schedule is disabled, deleted,
	// or stopped by a credential change before the agent receives the command.
	ScheduledExecStoppedTaskResult = "定时任务已停用，命令未投递/已取消"
)

type TaskResultParams struct {
	TaskID     string    `json:"task_id"`
	Result     string    `json:"result"`
	ExitCode   int       `json:"exit_code"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status,omitempty"`
}

type BasicInfoParams struct {
	Info         map[string]interface{} `json:"info"`
	ConfigState  *ConfigParams          `json:"config_state,omitempty"`
	ConfigResult *ConfigResultParams    `json:"config_result,omitempty"`
	Platform     string                 `json:"platform,omitempty"`
}

type PingResultParams struct {
	TaskID     uint      `json:"task_id"`
	PingType   string    `json:"ping_type"`
	Value      float64   `json:"value"`              // 延迟，单位毫秒。失败为负数。旧版 Agent 只发这个整数。
	ValueMS    *float64  `json:"value_ms,omitempty"` // 新版 Agent 的小数毫秒。有则优先使用。
	FinishedAt time.Time `json:"finished_at"`
}

// LatencyMS 优先使用新版 Agent 上报的小数毫秒，否则使用 value。
func (p PingResultParams) LatencyMS() float64 {
	if p.ValueMS != nil {
		return *p.ValueMS
	}
	return p.Value
}

type RouteHop struct {
	TTL       int     `json:"ttl"`
	IP        string  `json:"ip,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Timeout   bool    `json:"timeout,omitempty"`
}

type RouteResultParams struct {
	TaskID           uint       `json:"task_id"`
	Protocol         string     `json:"protocol"`
	Target           string     `json:"target"`
	IPVersion        int        `json:"ip_version"`
	Hops             []RouteHop `json:"hops,omitempty"`
	Error            string     `json:"error,omitempty"`
	ResolvedTargetIP string     `json:"resolved_target_ip,omitempty"`
	TargetReached    bool       `json:"target_reached,omitempty"`
	FinishedAt       time.Time  `json:"finished_at"`
}

type PullParams struct {
	Capabilities       []string       `json:"capabilities,omitempty"`
	CapabilityVersions map[string]int `json:"capability_versions,omitempty"`
	AckEventIDs        []string       `json:"ack_event_ids,omitempty"`
	LastEventID        string         `json:"last_event_id,omitempty"`
}

type ExecParams struct {
	TaskID  string `json:"task_id"`
	Command string `json:"command"`
}

type MCPExecParams struct {
	TaskID                 string    `json:"task_id"`
	LeaseID                string    `json:"lease_id"`
	OperationID            string    `json:"operation_id"`
	AgentUUID              string    `json:"agent_uuid"`
	Mode                   string    `json:"mode,omitempty"`
	Command                string    `json:"command"`
	Cwd                    string    `json:"cwd,omitempty"`
	ExpiresAt              time.Time `json:"expires_at"`
	OperationDeadline      time.Time `json:"operation_deadline"`
	ExecutionLeaseDeadline time.Time `json:"execution_lease_deadline"`
	RequestDigest          string    `json:"request_digest,omitempty"`
}

type MCPCancelParams struct {
	OperationID string `json:"operation_id"`
	LeaseID     string `json:"lease_id"`
}

type MCPRenewParams struct {
	LeaseID                string    `json:"lease_id"`
	ExecutionLeaseDeadline time.Time `json:"execution_lease_deadline"`
}

type MCPRevokeParams struct {
	LeaseID string `json:"lease_id"`
}

type MCPFileParams struct {
	TaskID                 string          `json:"task_id"`
	LeaseID                string          `json:"lease_id"`
	OperationID            string          `json:"operation_id"`
	Request                json.RawMessage `json:"request"`
	ExpiresAt              time.Time       `json:"expires_at"`
	OperationDeadline      time.Time       `json:"operation_deadline"`
	ExecutionLeaseDeadline time.Time       `json:"execution_lease_deadline"`
}

type PingParams struct {
	TaskID uint   `json:"ping_task_id"`
	Type   string `json:"ping_type"`
	Target string `json:"ping_target"`
}

type RouteParams struct {
	TaskID    uint   `json:"task_id"`
	Protocol  string `json:"protocol"`
	Target    string `json:"target"`
	IPVersion int    `json:"ip_version"`
	MaxHops   int    `json:"max_hops"`
}

type MessageParams struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type EventParams struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type RemoteRequestParams struct {
	RequestID string `json:"request_id"`
	Ticket    string `json:"ticket"`
}

type ConfigParams struct {
	Revision            uint64   `json:"revision,omitempty"`
	MonthRotate         *int     `json:"month_rotate,omitempty"`
	MonthRotateTime     *string  `json:"month_rotate_time,omitempty"`
	MonthRotateTimezone *string  `json:"month_rotate_timezone,omitempty"`
	Interval            *float64 `json:"interval,omitempty"`
	IncludeNics         *string  `json:"include_nics,omitempty"`
	ExcludeNics         *string  `json:"exclude_nics,omitempty"`
	IncludeMountpoints  *string  `json:"include_mountpoints,omitempty"`
	MemoryIncludeCache  *bool    `json:"memory_include_cache,omitempty"`
	EnableGPU           *bool    `json:"enable_gpu,omitempty"`
}

type ConfigResultParams struct {
	Revision uint64 `json:"revision"`
	EventID  string `json:"event_id,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

func Success(id any, result any) Response {
	return Response{JSONRPC: Version, ID: id, Result: result}
}

func Error(id any, code int, message string, data any) Response {
	return Response{JSONRPC: Version, ID: id, Error: &RPCError{Code: code, Message: message, Data: data}}
}
