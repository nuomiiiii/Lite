package admin

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/auditlog"
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
		api.RespondError(c, 400, "Invalid or missing request body: "+err.Error())
		return
	}
	for uuid := range ws.GetConnectedClients() {
		if contain(req.Clients, uuid) {
			onlineClients = append(onlineClients, uuid)
		} else {
			api.RespondError(c, 400, "Client not connected: "+uuid)
			return
		}
	}
	if len(onlineClients) == 0 {
		api.RespondError(c, 400, "No clients connected")
		return
	}
	taskId := utils.GenerateRandomString(16)
	if err := tasks.CreateTask(taskId, onlineClients, req.Command); err != nil {
		api.RespondError(c, 500, "Failed to create task: "+err.Error())
		return
	}
	for _, uuid := range onlineClients {
		var send struct {
			Message string `json:"message"`
			Command string `json:"command"`
			TaskId  string `json:"task_id"`
		}
		send.Message = "exec"
		send.Command = req.Command
		send.TaskId = taskId

		payload, _ := json.Marshal(send)
		client := ws.GetConnectedClients()[uuid]
		if client != nil {
			client.WriteMessage(websocket.TextMessage, payload)
		} else {
			api.RespondError(c, 400, "Client connection is null: "+uuid)
			return
		}
	}
	uuid, _ := c.Get("uuid")
	auditlog.Log(c.ClientIP(), uuid.(string), "REC, task id: "+taskId, "warn")
	api.RespondSuccess(c, gin.H{
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
