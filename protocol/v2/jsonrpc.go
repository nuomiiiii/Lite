package v2

import (
	"time"

	v1 "github.com/komari-monitor/komari/protocol/v1"
)

const (
	Version                 = "2.0"
	MethodAgentReport       = "agent.report"
	MethodAgentBasicInfo    = "agent.basicInfo"
	MethodAgentPingResult   = "agent.pingResult"
	MethodAgentRouteResult  = "agent.routeResult"
	MethodAgentTaskResult   = "agent.taskResult"
	MethodAgentExec         = "agent.exec"
	MethodAgentPing         = "agent.ping"
	MethodAgentRoute        = "agent.route"
	MethodAgentMessage      = "agent.message"
	MethodAgentEvent        = "agent.event"
	MethodAgentTerminal     = "agent.terminal.request"
	MethodAgentRemote       = "agent.remote.request"
	MethodAgentConfig       = "agent.config"
	MethodAgentConfigResult = "agent.configResult"
	MethodAgentPull         = "agent.pull"
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
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type ReportParams struct {
	Report      v1.Report `json:"report"`
	AckEventIDs []string  `json:"ack_event_ids,omitempty"`
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
	Value      int       `json:"value"`
	FinishedAt time.Time `json:"finished_at"`
}

type RouteHop struct {
	TTL       int     `json:"ttl"`
	IP        string  `json:"ip,omitempty"`
	LatencyMS float64 `json:"latency_ms,omitempty"`
	Timeout   bool    `json:"timeout,omitempty"`
}

type RouteResultParams struct {
	TaskID     uint       `json:"task_id"`
	Protocol   string     `json:"protocol"`
	Target     string     `json:"target"`
	IPVersion  int        `json:"ip_version"`
	Hops       []RouteHop `json:"hops,omitempty"`
	Error      string     `json:"error,omitempty"`
	FinishedAt time.Time  `json:"finished_at"`
}

type PullParams struct {
	Capabilities []string `json:"capabilities,omitempty"`
	AckEventIDs  []string `json:"ack_event_ids,omitempty"`
	LastEventID  string   `json:"last_event_id,omitempty"`
}

type ExecParams struct {
	TaskID  string `json:"task_id"`
	Command string `json:"command"`
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

type TerminalRequestParams struct {
	RequestID string `json:"request_id"`
}

type RemoteRequestParams struct {
	RequestID string `json:"request_id"`
	Ticket    string `json:"ticket"`
}

type ConfigParams struct {
	Revision           uint64   `json:"revision,omitempty"`
	MonthRotate        *int     `json:"month_rotate,omitempty"`
	Interval           *float64 `json:"interval,omitempty"`
	IncludeNics        *string  `json:"include_nics,omitempty"`
	ExcludeNics        *string  `json:"exclude_nics,omitempty"`
	IncludeMountpoints *string  `json:"include_mountpoints,omitempty"`
	MemoryIncludeCache *bool    `json:"memory_include_cache,omitempty"`
	EnableGPU          *bool    `json:"enable_gpu,omitempty"`
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
