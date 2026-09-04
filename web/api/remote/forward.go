package remote

import (
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/database/auditlog"
)

func forwardSession(session *remoteSession) {
	session.mu.Lock()
	browser := session.Browser
	agent := session.Agent
	startedAt := session.StartedAt
	session.mu.Unlock()
	if browser == nil || agent == nil {
		deleteSession(session.ID)
		return
	}
	auditlog.Log(session.RequesterIP, session.UserUUID, "established remote session, client:"+session.UUID, "terminal")
	errCh := make(chan error, 2)
	setAlive := func(connection *websocket.Conn) {
		_ = connection.SetReadDeadline(time.Now().Add(remoteIdleTimeout))
		connection.SetPongHandler(func(string) error {
			session.touch(time.Now())
			return connection.SetReadDeadline(time.Now().Add(remoteIdleTimeout))
		})
	}
	setAlive(browser)
	setAlive(agent)
	forward := func(source, target *websocket.Conn, auditFileWrites bool, browserSource bool) {
		for {
			messageType, data, err := source.ReadMessage()
			if err == nil {
				now := time.Now()
				session.touch(now)
				_ = source.SetReadDeadline(now.Add(remoteIdleTimeout))
				if browserSource && isRemoteHeartbeat(messageType, data) {
					continue
				}
				if auditFileWrites {
					if detail := fileOperationAuditDetail(data); detail != "" {
						auditlog.Log(session.RequesterIP, session.UserUUID, "remote file operation requested, client:"+session.UUID+", "+detail, "warn")
					}
				}
				err = target.WriteMessage(messageType, data)
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
			deadline := now.Add(5 * time.Second)
			if err := browser.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				waiting = false
				continue
			}
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
	deleteSession(session.ID)
	auditlog.Log(session.RequesterIP, session.UserUUID, "disconnected remote session, client:"+session.UUID+", duration:"+time.Since(startedAt).String(), "terminal")
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
