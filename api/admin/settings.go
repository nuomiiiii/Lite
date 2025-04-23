package admin

import (
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"

	"github.com/gin-gonic/gin"
)

// GetSettings 获取自定义配置
func GetSettings(c *gin.Context) {
	cst, err := config.Get()
	if err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}
	c.JSON(200, cst)
}

// EditSettings 更新自定义配置
func EditSettings(c *gin.Context) {
	var req map[string]interface{}

	db := dbcore.GetDBInstance()

	err := db.Model(&models.Client{}).Updates(req).Error
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "error": err})
		return
	}

	c.JSON(200, gin.H{"status": "success"})
}
