package remote

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
)

func forwardSession(session *remoteSession) {
	if session.mcp {
		forwardMCPSession(session)
		return
	}
	session.mu.Lock()
	browser := session.Browser
	agent := session.Agent
	startedAt := session.StartedAt
	session.mu.Unlock()
	if browser == nil || agent == nil {
		deleteSession(session.ID)
		return
	}
	auditlog.Event(session.RequesterIP, session.UserUUID, "terminal", "audit.remote_open", map[string]string{"name": clients.DisplayName(session.UUID)})
	errCh := make(chan error, 2)
	setAlive := func(connection *websocket.Conn, browserHop bool) {
		_ = connection.SetReadDeadline(time.Now().Add(remoteIdleTimeout))
		connection.SetPongHandler(func(string) error {
			now := time.Now()
			session.recordHopTraffic(browserHop, now)
			return connection.SetReadDeadline(now.Add(remoteIdleTimeout))
		})
	}
	setAlive(browser, true)
	setAlive(agent, false)
	forward := func(source, target *websocket.Conn, auditFileWrites bool, browserSource bool) {
		for {
			messageType, data, err := source.ReadMessage()
			if err == nil {
				now := time.Now()
				session.recordHopTraffic(browserSource, now)
				_ = source.SetReadDeadline(now.Add(remoteIdleTimeout))
				if browserSource && isRemoteHeartbeat(messageType, data) {
					continue
				}
				if auditFileWrites {
					if detail := fileOperationAuditDetail(data); detail != "" {
						auditlog.Event(session.RequesterIP, session.UserUUID, "warn", "audit.remote_file", map[string]string{
							"name":      clients.DisplayName(session.UUID),
							"operation": strings.TrimPrefix(detail, "operation:"),
						})
					}
				}
				err = target.WriteMessage(messageType, data)
				if err == nil {
					session.recordHopTraffic(!browserSource, now)
				}
			}
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
		}
	}
	go forward(browser, agent, true, true)
	go forward(agent, browser, false, false)
	timer := time.NewTimer(remoteMaxDuration)
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
			if !loginStillValid(session.UserUUID, session.LoginSession) {
				waiting = false
				continue
			}
			if !session.hopHasRecentTraffic(true, now) {
				if err := writeRemotePing(browser, now); pingWriteErrorEndsSession(err) {
					waiting = false
					continue
				}
			}
			if !session.hopHasRecentTraffic(false, now) {
				if err := writeRemotePing(agent, now); pingWriteErrorEndsSession(err) {
					waiting = false
				}
			}
		}
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	deleteSession(session.ID)
	auditlog.Event(session.RequesterIP, session.UserUUID, "terminal", "audit.remote_close", map[string]string{
		"name":     clients.DisplayName(session.UUID),
		"duration": time.Since(startedAt).String(),
	})
}

func isRemoteHeartbeat(messageType int, data []byte) bool {
	if messageType != websocket.TextMessage {
		return false
	}
	var message struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(data, &message) == nil && message.Type == "heartbeat"
}

func fileOperationAuditDetail(data []byte) string {
	var request struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &request) != nil {
		return ""
	}
	switch request.Type {
	case "file.create", "file.mkdir", "file.copy", "file.delete", "file.rename", "file.upload.start":
		return "operation:" + request.Type
	default:
		return ""
	}
}
