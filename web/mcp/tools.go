package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api/remote"
)

type toolError struct {
	message string
}

func (err toolError) Error() string { return err.message }

func errTool(message string) error { return toolError{message: message} }

var (
	lookupAuthorizedNode = requireAuthorizedNode
	mcpStartTerminal     = remote.StartMCPTerminal
	mcpTerminalAgentUUID = remote.MCPTerminalAgentUUID
	mcpWriteTerminal     = remote.WriteMCPTerminal
)

func toolDescriptors() []gin.H {
	agentUUID := schema("agent_uuid", "string", true)
	path := schema("path", "string", true)
	idem := schema("idempotency_key", "string", true)
	return []gin.H{
		tool("get_authorization", "Current MCP lease without secrets", gin.H{"type": "object", "additionalProperties": false, "properties": gin.H{}}),
		tool("list_agents", "Authorized nodes", objectSchema(gin.H{"q": schema("q", "string", false)}, nil)),
		tool("get_agent", "Authorized node details", objectSchema(gin.H{"agent_uuid": agentUUID}, []string{"agent_uuid"})),
		tool("exec", "Run a shell command on an authorized node", objectSchema(gin.H{
			"agent_uuid":      agentUUID,
			"command":         schema("command", "string", true),
			"cwd":             schema("cwd", "string", false),
			"timeout_seconds": schema("timeout_seconds", "integer", false),
			"idempotency_key": idem,
		}, []string{"agent_uuid", "command", "idempotency_key"})),
		tool("get_operation", "Read an operation owned by this lease", objectSchema(gin.H{"operation_id": schema("operation_id", "string", true)}, []string{"operation_id"})),
		tool("read_output", "Read operation output", objectSchema(gin.H{
			"operation_id": schema("operation_id", "string", true),
			"cursor":       schema("cursor", "integer", false),
			"max_bytes":    schema("max_bytes", "integer", false),
		}, []string{"operation_id"})),
		tool("cancel_operation", "Cancel a running operation", objectSchema(gin.H{"operation_id": schema("operation_id", "string", true)}, []string{"operation_id"})),
		tool("terminal_open", "Open an interactive terminal on an authorized node", objectSchema(gin.H{
			"agent_uuid":      agentUUID,
			"cols":            schema("cols", "integer", false),
			"rows":            schema("rows", "integer", false),
			"idempotency_key": idem,
		}, []string{"agent_uuid", "idempotency_key"})),
		tool("terminal_write", "Write to an open terminal", objectSchema(gin.H{
			"terminal_id":     schema("terminal_id", "string", true),
			"data":            schema("data", "string", true),
			"encoding":        schema("encoding", "string", false),
			"sequence":        schema("sequence", "integer", false),
			"idempotency_key": idem,
		}, []string{"terminal_id", "data", "idempotency_key"})),
		tool("terminal_read", "Read terminal output", objectSchema(gin.H{
			"terminal_id": schema("terminal_id", "string", true),
			"cursor":      schema("cursor", "integer", false),
			"max_bytes":   schema("max_bytes", "integer", false),
			"wait_ms":     schema("wait_ms", "integer", false),
		}, []string{"terminal_id"})),
		tool("terminal_resize", "Resize an open terminal", objectSchema(gin.H{
			"terminal_id": schema("terminal_id", "string", true),
			"cols":        schema("cols", "integer", true),
			"rows":        schema("rows", "integer", true),
		}, []string{"terminal_id", "cols", "rows"})),
		tool("terminal_close", "Close an open terminal", objectSchema(gin.H{"terminal_id": schema("terminal_id", "string", true)}, []string{"terminal_id"})),
		tool("file_roots", "Home directory and filesystem roots", objectSchema(gin.H{"agent_uuid": agentUUID}, []string{"agent_uuid"})),
		tool("file_list", "List a directory", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path, "cursor": schema("cursor", "integer", false), "limit": schema("limit", "integer", false)}, []string{"agent_uuid", "path"})),
		tool("file_stat", "Stat a path", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path}, []string{"agent_uuid", "path"})),
		tool("file_read", "Read a small file", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path, "offset": schema("offset", "integer", false), "max_bytes": schema("max_bytes", "integer", false), "encoding": schema("encoding", "string", false)}, []string{"agent_uuid", "path"})),
		tool("file_write", "Write a small file", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path, "content": schema("content", "string", true), "encoding": schema("encoding", "string", false), "overwrite": schema("overwrite", "boolean", false), "idempotency_key": idem}, []string{"agent_uuid", "path", "content", "idempotency_key"})),
		tool("file_mkdir", "Create a directory", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path, "recursive": schema("recursive", "boolean", false), "idempotency_key": idem}, []string{"agent_uuid", "path", "idempotency_key"})),
		tool("file_move", "Move or rename a path", objectSchema(gin.H{"agent_uuid": agentUUID, "source": schema("source", "string", true), "destination": schema("destination", "string", true), "overwrite": schema("overwrite", "boolean", false), "idempotency_key": idem}, []string{"agent_uuid", "source", "destination", "idempotency_key"})),
		tool("file_copy", "Copy a path", objectSchema(gin.H{"agent_uuid": agentUUID, "source": schema("source", "string", true), "destination": schema("destination", "string", true), "overwrite": schema("overwrite", "boolean", false), "idempotency_key": idem}, []string{"agent_uuid", "source", "destination", "idempotency_key"})),
		tool("file_delete", "Delete a path", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path, "recursive": schema("recursive", "boolean", true), "idempotency_key": idem}, []string{"agent_uuid", "path", "recursive", "idempotency_key"})),
		tool("file_upload_begin", "Begin a chunked upload", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path, "size": schema("size", "integer", true), "overwrite": schema("overwrite", "boolean", false), "idempotency_key": idem}, []string{"agent_uuid", "path", "size", "idempotency_key"})),
		tool("file_upload_chunk", "Upload one base64 chunk, at most 1 MiB decoded. Later offsets may be sent before earlier ones.", objectSchema(gin.H{"agent_uuid": agentUUID, "upload_id": schema("upload_id", "string", true), "data": schema("data", "string", true), "offset": schema("offset", "integer", true), "chunk_index": schema("chunk_index", "integer", false), "sha256": schema("sha256", "string", false)}, []string{"agent_uuid", "upload_id", "data", "offset"})),
		tool("file_upload_finish", "Finish a chunked upload", objectSchema(gin.H{"agent_uuid": agentUUID, "upload_id": schema("upload_id", "string", true)}, []string{"agent_uuid", "upload_id"})),
		tool("file_upload_cancel", "Cancel a chunked upload", objectSchema(gin.H{"agent_uuid": agentUUID, "upload_id": schema("upload_id", "string", true)}, []string{"agent_uuid", "upload_id"})),
		tool("file_download_begin", "Begin a chunked download", objectSchema(gin.H{"agent_uuid": agentUUID, "path": path}, []string{"agent_uuid", "path"})),
		tool("file_download_read", "Read a download chunk of at most 1 MiB", objectSchema(gin.H{"download_id": schema("download_id", "string", true), "offset": schema("offset", "integer", false), "max_bytes": schema("max_bytes", "integer", false)}, []string{"download_id"})),
		tool("file_download_close", "Close a download", objectSchema(gin.H{"download_id": schema("download_id", "string", true)}, []string{"download_id"})),
	}
}

func tool(name, description string, schema gin.H) gin.H {
	return gin.H{"name": name, "description": description, "inputSchema": schema}
}

func objectSchema(properties gin.H, required []string) gin.H {
	schema := gin.H{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func schema(description, kind string, _ bool) gin.H {
	return gin.H{"type": kind, "description": description}
}

func callTool(c *gin.Context, lease models.MCPLease, name string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	now := time.Now().UTC()
	live, err := loadLiveLease(lease.ID, now)
	if err != nil {
		return nil, err
	}
	lease = live
	switch name {
	case "get_authorization":
		return authorizationPayload(lease, now), nil
	case "list_agents":
		return listAuthorizedAgents(lease, stringArg(args, "q"))
	case "get_agent":
		return getAuthorizedAgent(lease, stringArg(args, "agent_uuid"))
	case "exec":
		return execTool(lease, args, now)
	case "get_operation":
		return getOperationTool(lease, stringArg(args, "operation_id"))
	case "read_output":
		return readOutputTool(lease, args)
	case "cancel_operation":
		return cancelOperationTool(lease, stringArg(args, "operation_id"))
	case "terminal_open":
		return terminalOpenTool(lease, args, now)
	case "terminal_write":
		return terminalWriteTool(lease, args)
	case "terminal_read":
		return terminalReadTool(lease, args)
	case "terminal_resize":
		return terminalResizeTool(lease, args)
	case "terminal_close":
		return terminalCloseTool(lease, stringArg(args, "terminal_id"))
	case "file_roots":
		return fileTool(lease, "file.roots", args, now, false)
	case "file_list":
		return fileTool(lease, "file.list", args, now, false)
	case "file_stat":
		return fileTool(lease, "file.stat", args, now, false)
	case "file_read":
		return fileTool(lease, "file.read", args, now, false)
	case "file_write":
		return fileTool(lease, "file.write", args, now, true)
	case "file_mkdir":
		return fileTool(lease, "file.mkdir", args, now, true)
	case "file_move":
		return fileTool(lease, "file.rename", args, now, true)
	case "file_copy":
		return fileTool(lease, "file.copy", args, now, true)
	case "file_delete":
		return fileTool(lease, "file.delete", args, now, true)
	case "file_upload_begin":
		return fileTool(lease, "file.upload.start", args, now, true)
	case "file_upload_chunk":
		return fileTool(lease, "file.upload.chunk", args, now, true)
	case "file_upload_finish":
		return fileTool(lease, "file.upload.finish", args, now, true)
	case "file_upload_cancel":
		return fileTool(lease, "file.upload.cancel", args, now, true)
	case "file_download_begin":
		return fileDownloadBeginTool(lease, args, now)
	case "file_download_read":
		return fileDownloadFollowTool(lease, "file.download.read", args, now, false)
	case "file_download_close":
		return fileDownloadFollowTool(lease, "file.download.close", args, now, true)
	default:
		_ = c
		return nil, errTool("unknown tool")
	}
}

func authorizationPayload(lease models.MCPLease, now time.Time) gin.H {
	remaining := int(lease.ExpiresAt.Sub(now) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return gin.H{
		"lease_id":          lease.ID,
		"mode":              lease.Mode,
		"long_term":         lease.LongTerm,
		"expires_at":        lease.ExpiresAt.UTC().Format(time.RFC3339),
		"remaining_seconds": remaining,
		"target_uuids":      parseTargetUUIDs(lease.TargetUUIDs),
		"max_concurrency":   lease.MaxConcurrency,
		"note":              lease.Note,
		"client_id":         lease.OAuthClientID,
	}
}

func listAuthorizedAgents(lease models.MCPLease, query string) (any, error) {
	ids := parseTargetUUIDs(lease.TargetUUIDs)
	nodes, err := clients.GetClientBasicInfoByUUIDs(ids)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]gin.H, 0, len(nodes))
	for _, node := range nodes {
		if query != "" && !strings.Contains(strings.ToLower(node.Name+" "+node.UUID+" "+node.OS), query) {
			continue
		}
		out = append(out, agentSummary(node))
	}
	return gin.H{"agents": out}, nil
}

func getAuthorizedAgent(lease models.MCPLease, uuid string) (any, error) {
	node, err := requireAuthorizedNode(lease, uuid)
	if err != nil {
		return nil, err
	}
	return agentSummary(node), nil
}

func agentSummary(node models.Client) gin.H {
	return gin.H{
		"uuid":             node.UUID,
		"name":             node.Name,
		"os":               node.OS,
		"arch":             node.Arch,
		"online":           agent_runtime.IsAgentOnline(node.UUID),
		"mcp_full":         node.MCPFull,
		"mcp_full_version": node.MCPFullVersion,
		"remote_enabled":   node.RemoteControlEnabled,
	}
}

func requireAuthorizedNode(lease models.MCPLease, uuid string) (models.Client, error) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return models.Client{}, errTool("agent_uuid is required")
	}
	if !leaseContainsNode(lease, uuid) {
		return models.Client{}, ErrNodeNotInLease
	}
	node, err := clients.GetClientByUUID(uuid)
	if err != nil {
		return models.Client{}, errTool("node not found")
	}
	if err := remote.AgentRemoteAllowed(node); err != nil {
		return models.Client{}, err
	}
	if !agentSupportsMCP(node) {
		return models.Client{}, ErrAgentUnsupported
	}
	return node, nil
}

func execTool(lease models.MCPLease, args map[string]any, now time.Time) (any, error) {
	node, err := requireAuthorizedNode(lease, stringArg(args, "agent_uuid"))
	if err != nil {
		return nil, err
	}
	command := stringArg(args, "command")
	if strings.TrimSpace(command) == "" {
		return nil, errTool("command is required")
	}
	if stringArg(args, "idempotency_key") == "" {
		return nil, errTool("idempotency_key is required")
	}
	if len(command) > 64<<10 {
		return nil, errTool("command is too long")
	}
	timeout := durationArg(args, "timeout_seconds", DefaultExecTimeout)
	deadline := now.Add(timeout)
	if deadline.After(lease.ExpiresAt) {
		deadline = lease.ExpiresAt
	}
	if !deadline.After(now) {
		return nil, ErrLeaseInactive
	}
	digest := requestDigest(map[string]any{
		"agent_uuid": node.UUID,
		"command":    command,
		"cwd":        stringArg(args, "cwd"),
		"timeout":    int(timeout / time.Second),
	})
	op, reused, err := createOperation(lease, node.UUID, "exec", stringArg(args, "idempotency_key"), digest, deadline)
	if err != nil {
		return nil, err
	}
	if !reused {
		params := v2.MCPExecParams{
			TaskID:                 op.ID,
			LeaseID:                lease.ID,
			OperationID:            op.ID,
			AgentUUID:              node.UUID,
			Mode:                   modeFull,
			Command:                command,
			Cwd:                    stringArg(args, "cwd"),
			ExpiresAt:              lease.ExpiresAt,
			OperationDeadline:      deadline,
			ExecutionLeaseDeadline: minTime(deadline, lease.ExpiresAt, now.Add(15*time.Second)),
			RequestDigest:          digest,
		}
		if !dispatchMCPExec(node.UUID, params) {
			expireOperation(op.ID, "the command could not be delivered")
			return nil, errTool("the command could not be delivered")
		}
		markOperationRunning(op.ID)
	}
	return gin.H{
		"operation_id": op.ID,
		"status":       op.State,
		"deadline":     op.Deadline.UTC().Format(time.RFC3339),
		"next_action":  "get_operation",
	}, nil
}

func getOperationTool(lease models.MCPLease, operationID string) (any, error) {
	op, err := loadOperation(lease.ID, operationID)
	if err != nil {
		return nil, err
	}
	return gin.H{
		"operation_id": op.ID,
		"agent_uuid":   op.AgentUUID,
		"tool_name":    op.ToolName,
		"status":       op.State,
		"exit_code":    op.ExitCode,
		"truncated":    op.Truncated,
		"deadline":     op.Deadline.UTC().Format(time.RFC3339),
		"started_at":   formatTimePtr(op.StartedAt),
		"finished_at":  formatTimePtr(op.FinishedAt),
	}, nil
}

func readOutputTool(lease models.MCPLease, args map[string]any) (any, error) {
	op, err := loadOperation(lease.ID, stringArg(args, "operation_id"))
	if err != nil {
		return nil, err
	}
	chunk, next, truncated := outputSlice(op.Output, intArg(args, "cursor"), intArg(args, "max_bytes"))
	return gin.H{
		"operation_id": op.ID,
		"stdout":       chunk,
		"cursor":       next,
		"truncated":    truncated || op.Truncated,
		"done":         op.FinishedAt != nil,
		"status":       op.State,
	}, nil
}

func cancelOperationTool(lease models.MCPLease, operationID string) (any, error) {
	op, err := loadOperation(lease.ID, operationID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_ = database().Model(&op).Updates(map[string]any{"state": opCancelRequested}).Error
	agent_runtime.DispatchV2Event(op.AgentUUID, v2.MethodAgentMCPCancel, v2.MCPCancelParams{
		OperationID: op.ID,
		LeaseID:     lease.ID,
	})
	return gin.H{"operation_id": op.ID, "status": opCancelRequested, "accepted_at": now.Format(time.RFC3339)}, nil
}

func terminalOpenTool(lease models.MCPLease, args map[string]any, now time.Time) (any, error) {
	node, err := lookupAuthorizedNode(lease, stringArg(args, "agent_uuid"))
	if err != nil {
		return nil, err
	}
	idem := stringArg(args, "idempotency_key")
	if idem == "" {
		return nil, errTool("idempotency_key is required")
	}
	cols := intArg(args, "cols")
	rows := intArg(args, "rows")
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	digest := requestDigest(map[string]any{
		"agent_uuid": node.UUID,
		"cols":       cols,
		"rows":       rows,
	})
	op, reused, err := createOperation(lease, node.UUID, "terminal.open", idem, digest, lease.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if reused {
		var retry bool
		op, retry, err = resolveIdempotentReuse(lease, op, true)
		if err != nil {
			return nil, err
		}
		if !retry {
			if !operationSucceeded(op) && op.FinishedAt != nil {
				return nil, finishedOperationError(op)
			}
			return gin.H{
				"terminal_id":  strings.TrimSpace(op.Output),
				"status":       op.State,
				"operation_id": op.ID,
				"expires_at":   lease.ExpiresAt.UTC().Format(time.RFC3339),
				"issued_at":    now.UTC().Format(time.RFC3339),
			}, nil
		}
	}
	sessionID, err := mcpStartTerminal(node.UUID, lease.OwnerUserUUID, lease.OwnerLoginSessionHash, lease.ID, lease.ExpiresAt, cols, rows)
	if err != nil {
		failOperation(op.ID, err.Error())
		return nil, err
	}
	CompleteOperation(op.ID, node.UUID, v2.TaskResultParams{TaskID: op.ID, Result: sessionID, ExitCode: 0, FinishedAt: time.Now().UTC()})
	return gin.H{
		"terminal_id":  sessionID,
		"status":       "accepted",
		"operation_id": op.ID,
		"expires_at":   lease.ExpiresAt.UTC().Format(time.RFC3339),
		"issued_at":    now.UTC().Format(time.RFC3339),
	}, nil
}

func terminalWriteTool(lease models.MCPLease, args map[string]any) (any, error) {
	id := stringArg(args, "terminal_id")
	uuid, err := mcpTerminalAgentUUID(id, lease.ID)
	if err != nil {
		return nil, err
	}
	idem := stringArg(args, "idempotency_key")
	if idem == "" {
		return nil, errTool("idempotency_key is required")
	}
	data, err := decodeToolBytes(rawStringArg(args, "data"), stringArg(args, "encoding"))
	if err != nil {
		return nil, err
	}
	digest := requestDigest(map[string]any{
		"terminal_id": id,
		"data":        rawStringArg(args, "data"),
		"encoding":    stringArg(args, "encoding"),
		"sequence":    intArg(args, "sequence"),
	})
	op, reused, err := rememberIdempotentOperation(lease, uuid, "terminal.write", idem, digest, lease.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if reused {
		var retry bool
		op, retry, err = resolveIdempotentReuse(lease, op, false)
		if err != nil {
			return nil, err
		}
		if !retry {
			if operationSucceeded(op) {
				return gin.H{"terminal_id": id, "accepted": true, "operation_id": op.ID}, nil
			}
			if op.FinishedAt == nil {
				return gin.H{"terminal_id": id, "accepted": false, "status": op.State, "operation_id": op.ID}, nil
			}
			return nil, finishedOperationError(op)
		}
	}
	if err := mcpWriteTerminal(id, data); err != nil {
		failOperation(op.ID, err.Error())
		return nil, err
	}
	CompleteOperation(op.ID, uuid, v2.TaskResultParams{TaskID: op.ID, Result: "accepted", ExitCode: 0, FinishedAt: time.Now().UTC()})
	return gin.H{"terminal_id": id, "accepted": true, "operation_id": op.ID}, nil
}

func terminalReadTool(lease models.MCPLease, args map[string]any) (any, error) {
	id := stringArg(args, "terminal_id")
	if err := remote.EnsureMCPTerminal(id, lease.ID); err != nil {
		return nil, err
	}
	chunk, next, closed := remote.ReadMCPTerminal(id, intArg(args, "cursor"), intArg(args, "max_bytes"))
	return gin.H{
		"terminal_id": id,
		"data":        base64.StdEncoding.EncodeToString(chunk),
		"encoding":    "base64",
		"cursor":      next,
		"closed":      closed,
		"ready":       remote.MCPTerminalReady(id),
	}, nil
}

func terminalResizeTool(lease models.MCPLease, args map[string]any) (any, error) {
	id := stringArg(args, "terminal_id")
	if err := remote.EnsureMCPTerminal(id, lease.ID); err != nil {
		return nil, err
	}
	if err := remote.ResizeMCPTerminal(id, intArg(args, "cols"), intArg(args, "rows")); err != nil {
		return nil, err
	}
	return gin.H{"terminal_id": id, "accepted": true}, nil
}

func terminalCloseTool(lease models.MCPLease, id string) (any, error) {
	if err := remote.EnsureMCPTerminal(id, lease.ID); err != nil {
		return nil, err
	}
	remote.CloseMCPTerminal(id)
	return gin.H{"terminal_id": id, "closed": true}, nil
}

func fileTool(lease models.MCPLease, fileType string, args map[string]any, now time.Time, mutating bool) (any, error) {
	node, err := requireAuthorizedNode(lease, stringArg(args, "agent_uuid"))
	if err != nil {
		return nil, err
	}
	deadline := now.Add(45 * time.Second)
	if deadline.After(lease.ExpiresAt) {
		deadline = lease.ExpiresAt
	}
	idem := stringArg(args, "idempotency_key")
	if mutating && idem == "" {
		if fileType == "file.upload.chunk" {
			if !hasArg(args, "offset") && !hasArg(args, "chunk_index") {
				return nil, errTool("offset is required")
			}
			idem = fileChunkIdempotencyKey(fileType, stringArg(args, "upload_id"), intArg(args, "offset"), intArg(args, "chunk_index"))
		} else if fileType == "file.upload.finish" || fileType == "file.upload.cancel" {
			idem = fileType + ":" + stringArg(args, "upload_id") + ":" + hashToken(rawStringArg(args, "data")+stringArg(args, "sha256"))
		} else if fileType == "file.download.close" {
			idem = fileType + ":" + stringArg(args, "download_id")
		} else {
			return nil, errTool("idempotency_key is required")
		}
	}
	if fileType == "file.upload.chunk" {
		if err := validateMCPUploadChunk(rawStringArg(args, "data")); err != nil {
			return nil, err
		}
	}
	request := map[string]any{
		"type":        fileType,
		"id":          "pending",
		"path":        firstNonEmpty(stringArg(args, "path"), stringArg(args, "source")),
		"destination": stringArg(args, "destination"),
		"upload_id":   stringArg(args, "upload_id"),
		"download_id": stringArg(args, "download_id"),
		"data":        rawStringArg(args, "data"),
		"sha256":      stringArg(args, "sha256"),
		"content":     rawStringArg(args, "content"),
		"encoding":    stringArg(args, "encoding"),
		"overwrite":   boolArg(args),
		"recursive":   boolArgNamed(args, "recursive"),
		"offset":      intArg(args, "offset") + intArg(args, "cursor"),
		"limit":       intArg(args, "limit"),
		"max_bytes":   intArg(args, "max_bytes"),
		"size":        intArg(args, "size"),
	}
	digest := requestDigest(request)
	op, reused, err := createOperation(lease, node.UUID, fileType, idem, digest, deadline)
	if err != nil {
		return nil, err
	}
	request["id"] = fileRequestID(op.ID)
	payload, _ := json.Marshal(request)
	if !reused {
		params := v2.MCPFileParams{
			TaskID:                 op.ID,
			LeaseID:                lease.ID,
			OperationID:            op.ID,
			Request:                payload,
			ExpiresAt:              lease.ExpiresAt,
			OperationDeadline:      deadline,
			ExecutionLeaseDeadline: minTime(deadline, lease.ExpiresAt, now.Add(15*time.Second)),
		}
		if !dispatchMCPFile(node.UUID, params) {
			expireOperation(op.ID, "the file request could not be delivered")
			return nil, errTool("the file request could not be delivered")
		}
		markOperationRunning(op.ID)
	}
	wait := waitOperation(op.ID)
	select {
	case <-wait:
	case <-time.After(25 * time.Second):
	}
	updated, err := loadOperation(lease.ID, op.ID)
	if err != nil {
		return nil, err
	}
	result := gin.H{
		"operation_id": updated.ID,
		"status":       updated.State,
		"exit_code":    updated.ExitCode,
	}
	if updated.Output != "" {
		var parsed any
		if json.Unmarshal([]byte(updated.Output), &parsed) == nil {
			result["result"] = parsed
		} else {
			result["result"] = updated.Output
		}
	}
	return result, nil
}

func fileDownloadBeginTool(lease models.MCPLease, args map[string]any, now time.Time) (any, error) {
	id, err := newID("dl_")
	if err != nil {
		return nil, err
	}
	args["download_id"] = id
	result, err := fileTool(lease, "file.download.begin", args, now, false)
	if err != nil {
		return nil, err
	}
	rememberDownload(lease.ID, stringArg(args, "agent_uuid"), id, lease.ExpiresAt)
	return result, nil
}

func fileDownloadFollowTool(lease models.MCPLease, fileType string, args map[string]any, now time.Time, mutating bool) (any, error) {
	id := stringArg(args, "download_id")
	ref, err := lookupDownload(lease.ID, id)
	if err != nil {
		return nil, err
	}
	args["agent_uuid"] = ref.AgentUUID
	result, err := fileTool(lease, fileType, args, now, mutating)
	if err == nil && fileType == "file.download.close" {
		forgetDownload(lease.ID, id)
	}
	return result, err
}

type mcpDownloadRef struct {
	LeaseID   string
	AgentUUID string
	ExpiresAt time.Time
}

var (
	mcpDLMu sync.Mutex
	mcpDLs  = map[string]mcpDownloadRef{}
)

func downloadKey(leaseID, downloadID string) string {
	return leaseID + "\n" + downloadID
}

func rememberDownload(leaseID, agentUUID, downloadID string, expiresAt time.Time) {
	leaseID = strings.TrimSpace(leaseID)
	downloadID = strings.TrimSpace(downloadID)
	agentUUID = strings.TrimSpace(agentUUID)
	if leaseID == "" || downloadID == "" || agentUUID == "" {
		return
	}
	mcpDLMu.Lock()
	mcpDLs[downloadKey(leaseID, downloadID)] = mcpDownloadRef{
		LeaseID:   leaseID,
		AgentUUID: agentUUID,
		ExpiresAt: expiresAt,
	}
	mcpDLMu.Unlock()
}

func lookupDownload(leaseID, downloadID string) (mcpDownloadRef, error) {
	mcpDLMu.Lock()
	ref, ok := mcpDLs[downloadKey(leaseID, downloadID)]
	mcpDLMu.Unlock()
	if !ok || ref.AgentUUID == "" || time.Now().UTC().After(ref.ExpiresAt) {
		return mcpDownloadRef{}, errTool("download session not found")
	}
	return ref, nil
}

func forgetDownload(leaseID, downloadID string) {
	mcpDLMu.Lock()
	delete(mcpDLs, downloadKey(leaseID, downloadID))
	mcpDLMu.Unlock()
}

func stringArg(args map[string]any, key string) string {
	return strings.TrimSpace(rawStringArg(args, key))
}

func rawStringArg(args map[string]any, key string) string {
	raw, _ := args[key].(string)
	return raw
}

func hasArg(args map[string]any, key string) bool {
	_, ok := args[key]
	return ok
}

func fileChunkIdempotencyKey(fileType, uploadID string, offset, chunkIndex int) string {
	return fileType + ":" + uploadID + ":" + strconv.Itoa(offset) + ":" + strconv.Itoa(chunkIndex)
}

func intArg(args map[string]any, key string) int {
	switch raw := args[key].(type) {
	case int:
		return raw
	case int64:
		return int(raw)
	case float64:
		return int(raw)
	}
	return 0
}

func boolArg(args map[string]any) bool {
	return boolArgNamed(args, "overwrite")
}

func boolArgNamed(args map[string]any, key string) bool {
	raw, _ := args[key].(bool)
	return raw
}

func durationArg(args map[string]any, key string, fallback time.Duration) time.Duration {
	seconds := intArg(args, key)
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

const maxMCPUploadChunkBytes = 1 << 20

func validateMCPUploadChunk(data string) error {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return errTool("invalid upload chunk")
	}
	if len(decoded) == 0 {
		return errTool("upload chunk is empty")
	}
	if len(decoded) > maxMCPUploadChunkBytes {
		return errTool("upload chunk is too large")
	}
	return nil
}

func decodeToolBytes(data, encoding string) ([]byte, error) {
	if strings.EqualFold(strings.TrimSpace(encoding), "base64") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
		if err != nil {
			return nil, errTool("invalid base64 data")
		}
		return decoded, nil
	}
	return []byte(data), nil
}

func formatTimePtr(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func minTime(values ...time.Time) time.Time {
	var earliest time.Time
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		if earliest.IsZero() || value.Before(earliest) {
			earliest = value
		}
	}
	return earliest
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
