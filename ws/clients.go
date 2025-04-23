package ws

import (
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/gin-gonic/gin"
)

func GetClients(c *gin.Context) {
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

	for {
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

		var resp = map[string]interface{}{
			"status": "success",
			"last":   LatestReport,
			"online": func() []string {
				keys := make([]string, 0, len(ConnectedClients))
				for key := range ConnectedClients {
					keys = append(keys, key)
				}
				return keys
			}(),
		}
		err = conn.WriteJSON(gin.H{"status": "success", "data": resp})
		if err != nil {
			return
		}
	}

}
