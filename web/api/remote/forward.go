package remote

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/auditlog"
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
	forward := func(source, target interface {
		ReadMessage() (int, []byte, error)
		WriteMessage(int, []byte) error
	}, auditFileWrites bool) {
		for {
			messageType, data, err := source.ReadMessage()
			if err == nil {
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
	go forward(browser, agent, true)
	go forward(agent, browser, false)
	timer := time.NewTimer(remoteMaxDuration)
	select {
	case <-errCh:
	case <-timer.C:
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

func fileOperationAuditDetail(data []byte) string {
	var request struct {
		Type        string `json:"type"`
		Path        string `json:"path"`
		Destination string `json:"destination"`
	}
	if json.Unmarshal(data, &request) != nil {
		return ""
	}
	switch request.Type {
	case "file.create", "file.mkdir", "file.delete", "file.rename", "file.upload.start":
	default:
		return ""
	}
	detail := "operation:" + request.Type + ", path:" + sanitizeAuditPath(request.Path)
	if request.Destination != "" {
		detail += ", destination:" + sanitizeAuditPath(request.Destination)
	}
	return detail
}

func sanitizeAuditPath(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
	const maxLength = 320
	characters := []rune(value)
	if len(characters) > maxLength {
		value = string(characters[:maxLength]) + "..."
	}
	return value
}
