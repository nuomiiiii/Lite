package remote

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

const mcpTerminalOutputLimit = 16 << 20

func StartMCPTerminal(uuid, userUUID, loginSession, leaseID string, deadline time.Time, cols, rows int) (string, error) {
	if !RemoteManagementEnabled() {
		return "", errRemoteManagementDisabled
	}
	if !agent_runtime.IsAgentOnline(uuid) {
		return "", errRemoteClientOffline
	}
	now := time.Now()
	if !deadline.After(now) {
		return "", errors.New("MCP authorization has expired")
	}
	session := &remoteSession{
		ID:           utils.GenerateRandomString(32),
		UUID:         uuid,
		UserUUID:     userUUID,
		LoginSession: loginSession,
		AgentTicket:  utils.GenerateRandomString(32),
		CreatedAt:    now,
		ExpiresAt:    deadline.UTC(),
		LastActivity: now,
		mcp:          true,
		leaseID:      leaseID,
	}
	if session.ID == "" || session.AgentTicket == "" {
		return "", errors.New("failed to create MCP terminal")
	}
	if err := putSessionUnderDeliveryGate(session); err != nil {
		return "", err
	}
	params := v2.RemoteRequestParams{RequestID: session.ID, Ticket: session.pendingAgentTicket()}
	if err := dispatchRemoteRequest(uuid, params); err != nil {
		deleteSession(session.ID)
		return "", err
	}
	if cols > 0 && rows > 0 {
		_ = ResizeMCPTerminal(session.ID, cols, rows)
	}
	return session.ID, nil
}

func EnsureMCPTerminal(sessionID, leaseID string) error {
	session := getSession(sessionID)
	if session == nil || !session.mcp || session.leaseID != leaseID {
		return errors.New("terminal not found")
	}
	if time.Now().After(session.ExpiresAt) {
		return errors.New("terminal has expired")
	}
	return nil
}

func MCPTerminalAgentUUID(sessionID, leaseID string) (string, error) {
	if err := EnsureMCPTerminal(sessionID, leaseID); err != nil {
		return "", err
	}
	session := getSession(sessionID)
	if session == nil {
		return "", errors.New("terminal not found")
	}
	return session.UUID, nil
}

func MCPTerminalReady(sessionID string) bool {
	session := getSession(sessionID)
	if session == nil {
		return false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.mcp && session.Agent != nil && !session.closed
}

func WriteMCPTerminal(sessionID string, data []byte) error {
	session := getSession(sessionID)
	if session == nil || !session.mcp {
		return errors.New("terminal not found")
	}
	session.mu.Lock()
	agent := session.Agent
	session.mu.Unlock()
	if agent == nil {
		return errors.New("terminal is not ready")
	}
	session.touch(time.Now())
	return agent.WriteMessage(websocket.BinaryMessage, data)
}

func ResizeMCPTerminal(sessionID string, cols, rows int) error {
	session := getSession(sessionID)
	if session == nil || !session.mcp {
		return errors.New("terminal not found")
	}
	session.mu.Lock()
	agent := session.Agent
	session.mu.Unlock()
	if agent == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	return agent.WriteMessage(websocket.TextMessage, payload)
}

func ReadMCPTerminal(sessionID string, cursor, maxBytes int) (data []byte, next int, closed bool) {
	session := getSession(sessionID)
	if session == nil {
		return nil, cursor, true
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if cursor < session.mcpBase {
		cursor = session.mcpBase
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	if maxBytes > 256<<10 {
		maxBytes = 256 << 10
	}
	offset := cursor - session.mcpBase
	if offset < 0 {
		offset = 0
	}
	if offset > len(session.mcpOut) {
		offset = len(session.mcpOut)
	}
	end := offset + maxBytes
	if end > len(session.mcpOut) {
		end = len(session.mcpOut)
	}
	chunk := append([]byte(nil), session.mcpOut[offset:end]...)
	return chunk, session.mcpBase + end, session.mcpClosed || session.closed
}

func CloseMCPTerminal(sessionID string) {
	deleteSession(sessionID)
}

func appendMCPOutput(session *remoteSession, data []byte) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.mcpOut = append(session.mcpOut, data...)
	if len(session.mcpOut) > mcpTerminalOutputLimit {
		drop := len(session.mcpOut) - mcpTerminalOutputLimit
		session.mcpOut = append([]byte(nil), session.mcpOut[drop:]...)
		session.mcpBase += drop
	}
	session.LastActivity = time.Now()
}

func forwardMCPSession(session *remoteSession) {
	session.mu.Lock()
	agent := session.Agent
	startedAt := session.StartedAt
	session.mu.Unlock()
	if agent == nil {
		deleteSession(session.ID)
		return
	}
	auditlog.Event(session.RequesterIP, session.UserUUID, "terminal", "audit.mcp_terminal_open", map[string]string{"name": clients.DisplayName(session.UUID)})
	_ = agent.SetReadDeadline(session.ExpiresAt)
	errCh := make(chan error, 1)
	go func() {
		for {
			messageType, data, err := agent.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			session.touch(time.Now())
			if messageType == websocket.BinaryMessage || messageType == websocket.TextMessage {
				appendMCPOutput(session, data)
			}
		}
	}()
	timer := time.NewTimer(time.Until(session.ExpiresAt))
	pingTicker := time.NewTicker(remotePingInterval)
	defer pingTicker.Stop()
	waiting := true
	for waiting {
		select {
		case <-errCh:
			waiting = false
		case <-timer.C:
			waiting = false
		case now := <-pingTicker.C:
			if !loginStillValid(session.UserUUID, session.LoginSession) || now.After(session.ExpiresAt) {
				waiting = false
				continue
			}
			deadline := now.Add(5 * time.Second)
			if err := agent.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				waiting = false
			}
		}
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	session.mu.Lock()
	session.mcpClosed = true
	session.mu.Unlock()
	deleteSession(session.ID)
	auditlog.Event(session.RequesterIP, session.UserUUID, "terminal", "audit.mcp_terminal_close", map[string]string{
		"name":     clients.DisplayName(session.UUID),
		"duration": time.Since(startedAt).String(),
	})
}
