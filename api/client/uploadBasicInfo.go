package client

import (
	"github.com/akizon77/komari/database/clients"

	"github.com/gin-gonic/gin"
)

func UploadBasicInfo(c *gin.Context) {
	var cbi = &clients.ClientBasicInfo{}
	err := c.ShouldBindJSON(&cbi)
	if err != nil {
		c.JSON(400, gin.H{"status": "error", "error": err.Error()})
		return
	}
	err = clients.UpdateOrInsertBasicInfo(*cbi)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "success"})
}
