package admin

import (
	"github.com/akizon77/komari/database/accounts"

	"github.com/gin-gonic/gin"
)

func GetSessions(c *gin.Context) {

	ss, err := accounts.GetAllSessions()
	if err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Failed to get sessions",
		})
		return
	}
	c.JSON(200, ss)
}

func DeleteSession(c *gin.Context) {
	var req struct {
		Session string `json:"session" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing session",
		})
		return
	}
	err := accounts.DeleteSession(req.Session)
	if err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Failed to delete session",
		})
		return
	}

	c.JSON(200, gin.H{
		"status":  "success",
		"message": "Session deleted successfully",
	})
}
