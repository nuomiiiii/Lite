package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func OrderWeight(c *gin.Context) {
	var req = make(map[string]int)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing request body",
		})
		return
	}
	db := dbcore.GetDBInstance()
	for uuid, weight := range req {
		err := db.Model(&models.Client{}).Where("uuid = ?", uuid).Update("weight", weight).Error
		if err != nil {
			c.JSON(500, gin.H{
				"status": "error",
				"error":  "Failed to update client weight",
			})
			return
		}
	}
	c.JSON(200, gin.H{
		"status":  "success",
		"message": "Client weight updated successfully",
	})
}
