package api

import (
	"github.com/gin-gonic/gin"
)

func GetClientRecentRecords(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
		return
	}
	c.JSON(200, Records[uuid])
}
