package admin

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/ws"
)

// 接受数据类型：
// - command: string
// - clients: []string (客户端 UUID 列表)
func Exec(c *gin.Context) {
	var req struct {
		Command string   `json:"command" binding:"required"`
		Clients []string `json:"clients" binding:"required"`
	}
	var onlineClients []string
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"status": "error", "message": "Invalid request"})
		return
	}
	for uuid := range ws.ConnectedClients {
		if contain(req.Clients, uuid) {
			onlineClients = append(onlineClients, uuid)
		} else {
			c.JSON(400, gin.H{"status": "error", "message": "Client not connected: " + uuid})
			return
		}
	}
	if len(onlineClients) == 0 {
		c.JSON(400, gin.H{"status": "error", "message": "No clients connected"})
		return
	}
	taskId := utils.GenerateRandomString(16)
	if err := tasks.CreateTask(taskId, onlineClients, req.Command); err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to create task: " + err.Error()})
		return
	}
	for _, uuid := range onlineClients {
		var send struct {
			Command string `json:"command"`
			TaskId  string `json:"task_id"`
		}
		send.Command = req.Command
		send.TaskId = taskId

		payload, _ := json.Marshal(send)
		client := ws.ConnectedClients[uuid]
		if client != nil {
			client.WriteMessage(websocket.TextMessage, payload)
		} else {
			c.JSON(400, gin.H{"status": "error", "message": "Client connection is null: " + uuid})
			return
		}
	}
	c.JSON(200, gin.H{
		"status":  "success",
		"message": "Command sent to clients",
		"task_id": taskId,
		"clients": onlineClients,
	})
}
func contain(clients []string, uuid string) bool {
	for _, client := range clients {
		if client == uuid {
			return true
		}
	}
	return false
}
