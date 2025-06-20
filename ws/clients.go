package ws

import (
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/common"

	"github.com/gin-gonic/gin"
)

func GetClients(c *gin.Context) {
	// 升级到ws
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

	// 请求
	for {
		var resp struct {
			Online []string                  `json:"online"` // 已建立连接的客户端uuid列表
			Data   map[string]*common.Report `json:"data"`   // 最后上报的数据
		}
		resp.Online = []string{}
		_, data, err := conn.ReadMessage()
		if err != nil {
			//log.Println("Error reading message:", err)
			return
		}
		message := string(data)

		if message != "get" {
			conn.WriteJSON(gin.H{"status": "error", "error": "Invalid message"})
			continue
		}
		// 已建立连接的客户端uuid列表
		for key := range GetConnectedClients() {
			resp.Online = append(resp.Online, key)
		}
		// 清除UUID，简化报告单
		resp.Data = GetLatestReport()
		for _, report := range resp.Data {
			report.UUID = ""
		}
		for _, report := range resp.Data {
			if report.CPU.Usage == 0 {
				report.CPU.Usage = 0.01
			}
		}
		err = conn.WriteJSON(gin.H{"status": "success", "data": resp})
		if err != nil {
			return
		}
	}

}
