package admin

import (
	"database/sql"

	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/models"

	"github.com/gin-gonic/gin"
)

// GetSettings 获取自定义配置
func GetSettings(c *gin.Context) {
	cst, err := config.Get()
	if err != nil {
		if err == sql.ErrNoRows {
			//override
			cst = models.Config{Sitename: "Komari"}
			cst.ID = 1
			config.Save(cst)
			c.JSON(200, cst)
			return
		}
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Internal Server Error: " + err.Error(),
		})
	}
	c.JSON(200, cst)
}

// EditSettings 更新自定义配置
func EditSettings(c *gin.Context) {
	cfg := make(map[string]interface{})
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Bad Request: " + err.Error(),
		})
		return
	}

	cfg["id"] = 1 // Only one record
	if err := config.Update(cfg); err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Internal Server Error: " + err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{"status": "success"})
}
