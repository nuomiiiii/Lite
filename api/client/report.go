package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/utils/notification"
	"github.com/komari-monitor/komari/ws"
)

func UploadReport(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Println("Failed to read request body:", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	var data map[string]interface{}
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	// Save report to database
	var report common.Report
	err = json.Unmarshal(bodyBytes, &report)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	report.UpdatedAt = time.Now()
	err = SaveClientReport(report.UUID, report)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%v", err)})
		return
	}
	// Update report with method and token

	ws.LatestReport[report.UUID] = &report

	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // Restore the body for further use
	c.JSON(200, gin.H{"status": "success"})
}

func WebSocketReport(c *gin.Context) {
	// 升级ws
	if !websocket.IsWebSocketUpgrade(c.Request) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Require WebSocket upgrade"})
		return
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	// Upgrade the HTTP connection to a WebSocket connection
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Failed to upgrade to WebSocket"})
		return
	}
	defer conn.Close()

	_, message, err := conn.ReadMessage()
	if err != nil {
		log.Println("Error reading message:", err)
		return
	}

	// 第一次数据拿token
	data := map[string]interface{}{}
	err = json.Unmarshal(message, &data)
	if err != nil {
		conn.WriteJSON(gin.H{"status": "error", "error": "Invalid JSON"})
		return
	}
	// it should ok,token was verfied in the middleware
	token := ""
	var errMsg string

	// 优先检查查询参数中的 token
	token = c.Query("token")

	// 如果 token 为空，返回错误
	if token == "" {
		conn.WriteJSON(gin.H{"status": "error", "error": errMsg})
		return
	}

	uuid, err := clients.GetClientUUIDByToken(token)
	if err != nil {
		conn.WriteJSON(gin.H{"status": "error", "error": errMsg})
		return
	}

	// 只允许一个客户端的连接
	if _, exists := ws.ConnectedClients[uuid]; exists {
		conn.WriteJSON(gin.H{"status": "error", "error": "Token already in use"})
		return
	}
	ws.ConnectedClients[uuid] = conn
	go notification.OnlineNotification(uuid)
	defer func() {
		delete(ws.ConnectedClients, uuid)
		notification.OfflineNotification(uuid)
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		report := common.Report{}
		err = json.Unmarshal(message, &report)
		if err != nil {
			break
		}
		report.UpdatedAt = time.Now()
		err = SaveClientReport(uuid, report)
		if err != nil {
			conn.WriteJSON(gin.H{"status": "error", "error": fmt.Sprintf("%v", err)})
		}

		ws.LatestReport[uuid] = &report
	}
}

func SaveClientReport(uuid string, report common.Report) error {
	if api.Records[uuid] == nil {
		api.Records[uuid] = []common.Report{}
	}
	if report.CPU.Usage < 0.01 {
		report.CPU.Usage = 0.01
	}
	api.Records[uuid] = append(api.Records[uuid], report)

	return nil
}
