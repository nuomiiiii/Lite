package client

import (
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/clients"

	"github.com/gin-gonic/gin"
)

func UploadBasicInfo(c *gin.Context) {
	var cbi = common.ClientInfo{}
	if err := c.ShouldBindJSON(&cbi); err != nil {
		c.JSON(400, gin.H{"status": "error", "error": "Invalid or missing data"})
		return
	}

	token := c.Query("token")
	client, err := clients.GetClientByUUID(token)
	if client.UUID == "" || err != nil {
		c.JSON(400, gin.H{"status": "error", "error": "Invalid token"})
		return
	}

	cbi.ClientUUID = client.UUID
	if err := clients.UpdateOrInsertBasicInfo(cbi); err != nil {
		c.JSON(500, gin.H{"status": "error", "error": "Failed to update client info"})
		return
	}

	c.JSON(200, gin.H{"status": "success"})
}
