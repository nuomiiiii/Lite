package terminal

import (
	"sync"

	"github.com/gorilla/websocket"
)

type TerminalSession struct {
	UUID        string
	UserUUID    string
	Browser     *websocket.Conn
	Agent       *websocket.Conn
	RequesterIp string
}

var TerminalSessionsMutex = &sync.Mutex{}
var TerminalSessions = make(map[string]*TerminalSession)

func CloseClientSessions(uuid string) {
	TerminalSessionsMutex.Lock()
	connections := make([]*websocket.Conn, 0)
	for id, session := range TerminalSessions {
		if session.UUID != uuid {
			continue
		}
		delete(TerminalSessions, id)
		if session.Browser != nil {
			connections = append(connections, session.Browser)
		}
		if session.Agent != nil {
			connections = append(connections, session.Agent)
		}
	}
	TerminalSessionsMutex.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}
