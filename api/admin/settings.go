package admin

import (
	"database/sql"

	"github.com/akizon77/komari/database/custom"
	"github.com/akizon77/komari/database/models"

	"log"

	"github.com/gin-gonic/gin"
)

// GetSettings 获取自定义配置
func GetSettings(c *gin.Context) {
	cst, err := custom.Get()
	if err != nil {
		if err == sql.ErrNoRows {
			//override
			cst = models.Custom{SiteName: "Komari"}
			custom.Save(cst)
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
	var req models.Custom
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("Invalid request body: %v", err)
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid request body",
		})
		return
	}

	if err := custom.Save(req); err != nil {
		log.Printf("Failed to save custom config: %v", err)
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Internal Server Error" + err.Error(),
		})
		return
	}

	c.JSON(200, gin.H{"status": "success"})
}
