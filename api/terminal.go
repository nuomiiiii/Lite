package api

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/ws"
)

func RequestTerminal(c *gin.Context) {
	uuid := c.Param("uuid")
	_, err := clients.GetClientByUUID(uuid)
	if err != nil {
		c.JSON(400, gin.H{
			"status":  "error",
			"message": "Client not found",
		})
		return
	}
	// 建立ws
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": "Require WebSocket upgrade"})
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // TODO: 检查源站
		},
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	// 新建一个终端连接
	id := utils.GenerateRandomString(32)
	session := &TerminalSession{
		UUID:    uuid,
		Browser: conn,
		Agent:   nil,
	}

	TerminalSessionsMutex.Lock()
	TerminalSessions[id] = session
	TerminalSessionsMutex.Unlock()
	conn.SetCloseHandler(func(code int, text string) error {
		log.Println("Terminal connection closed:", code, text)
		TerminalSessionsMutex.Lock()
		delete(TerminalSessions, id)
		TerminalSessionsMutex.Unlock()
		// 通知 Agent 关闭终端连接
		if session.Agent != nil {
			session.Agent.Close()
		}
		return nil
	})

	if ws.ConnectedClients[uuid] == nil {
		conn.WriteMessage(1, []byte("Client offline!"))
		conn.Close()
		TerminalSessionsMutex.Lock()
		delete(TerminalSessions, id)
		TerminalSessionsMutex.Unlock()
		return
	}
	err = ws.ConnectedClients[uuid].WriteJSON(gin.H{
		"message":    "terminal",
		"request_id": id,
	})
	if err != nil {
		conn.Close()
		TerminalSessionsMutex.Lock()
		delete(TerminalSessions, id)
		TerminalSessionsMutex.Unlock()
		return
	}
	conn.WriteMessage(1, []byte("waiting for agent..."))
	// 如果没有连接上，则关闭连接
	time.AfterFunc(30*time.Second, func() {
		TerminalSessionsMutex.Lock()
		if session.Agent == nil {
			if session.Browser != nil {
				session.Browser.WriteMessage(1, []byte("timeout"))
				session.Browser.Close()
			}
			conn.Close()
			delete(TerminalSessions, id)
		}
		TerminalSessionsMutex.Unlock()
	})
}

func ForwardTerminal(id string) {
	session, exists := TerminalSessions[id]
	if !exists || session == nil || session.Agent == nil || session.Browser == nil {
		return
	}
	errChan := make(chan error, 1)

	go func() {
		for {
			messageType, data, err := session.Browser.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}

			if messageType == websocket.TextMessage {
				if session.Agent != nil && string(data[0:1]) == "{" {
					err = session.Agent.WriteMessage(websocket.TextMessage, data)
				} else if session.Agent != nil {
					err = session.Agent.WriteMessage(websocket.BinaryMessage, data)
				}
			} else if session.Agent != nil {
				// 二进制消息，原样传递
				err = session.Agent.WriteMessage(websocket.BinaryMessage, data)
			}

			if err != nil {
				errChan <- err
				return
			}
		}
	}()

	go func() {
		for {
			_, data, err := session.Agent.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			if session.Browser != nil {
				err = session.Browser.WriteMessage(websocket.BinaryMessage, data)
				if err != nil {
					errChan <- err
					return
				}
			}
		}
	}()

	// 等待错误或主动关闭
	<-errChan
	// 关闭连接
	if session.Agent != nil {
		session.Agent.Close()
	}
	if session.Browser != nil {
		session.Browser.Close()
	}
	TerminalSessionsMutex.Lock()
	delete(TerminalSessions, id)
	TerminalSessionsMutex.Unlock()
}
