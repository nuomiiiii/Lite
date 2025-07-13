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
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/utils/notifier"
	"github.com/komari-monitor/komari/ws"
	"github.com/patrickmn/go-cache"
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

	ws.GetLatestReport()[report.UUID] = &report

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
	unsafeConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Failed to upgrade to WebSocket"})
		return
	}
	conn := ws.NewSafeConn(unsafeConn)
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
	if _, exists := ws.GetConnectedClients()[uuid]; exists {
		conn.WriteJSON(gin.H{"status": "error", "error": "Token already in use"})
		return
	}
	ws.SetConnectedClients(uuid, conn)
	go notifier.OnlineNotification(uuid)
	defer func() {
		ws.DeleteConnectedClients(uuid)
		notifier.OfflineNotification(uuid)
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		type MessageType struct {
			Type string `json:"type"`
		}
		var msgType MessageType
		err = json.Unmarshal(message, &msgType)
		if err != nil {
			conn.WriteJSON(gin.H{"status": "error", "error": "Invalid JSON"})
			continue
		}
		switch msgType.Type {
		case "", "report":
			report := common.Report{}
			err = json.Unmarshal(message, &report)
			if err != nil {
				conn.WriteJSON(gin.H{"status": "error", "error": "Invalid report format"})
				continue
			}
			report.UpdatedAt = time.Now()
			err = SaveClientReport(uuid, report)
			if err != nil {
				conn.WriteJSON(gin.H{"status": "error", "error": fmt.Sprintf("%v", err)})
				continue
			}
			ws.SetLatestReport(uuid, &report)
		case "ping_result":
			var reqBody struct {
				PingTaskID uint      `json:"task_id"`
				PingResult int       `json:"value"`
				PingType   string    `json:"ping_type"`
				FinishedAt time.Time `json:"finished_at"`
			}
			err = json.Unmarshal(message, &reqBody)
			if err != nil {
				conn.WriteJSON(gin.H{"status": "error", "error": "Invalid ping result format"})
				continue
			}
			pingResult := models.PingRecord{
				Client: uuid,
				TaskId: reqBody.PingTaskID,
				Value:  reqBody.PingResult,
				Time:   models.FromTime(reqBody.FinishedAt),
			}
			tasks.SavePingRecord(pingResult)
		default:
			log.Printf("Unknown message type: %s", msgType.Type)
			conn.WriteJSON(gin.H{"status": "error", "error": "Unknown message type"})
		}

	}
}

func SaveClientReport(uuid string, report common.Report) error {
	reports, _ := api.Records.Get(uuid)
	if reports == nil {
		reports = []common.Report{}
	}
	if report.CPU.Usage < 0.01 {
		report.CPU.Usage = 0.01
	}
	reports = append(reports.([]common.Report), report)
	api.Records.Set(uuid, reports, cache.DefaultExpiration)

	return nil
}
