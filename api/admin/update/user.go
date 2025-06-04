package update

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
)

func UpdateUser(c *gin.Context) {
	var req struct {
		Uuid     string  `json:"uuid" binding:"required"`
		Name     *string `json:"username"`
		Password *string `json:"password"`
		SsoType  *string `json:"sso_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"status": "error", "message": err.Error()})
		return
	}
	if req.Password == nil && req.Name == nil {
		c.JSON(400, gin.H{"status": "error", "message": "At least one field (username or password) must be provided"})
		return
	}
	if req.Name != nil && len(*req.Name) < 3 {
		c.JSON(400, gin.H{"status": "error", "message": "Username must be at least 3 characters long"})
		return
	}
	if req.Password != nil && len(*req.Password) < 6 {
		c.JSON(400, gin.H{"status": "error", "message": "Password must be at least 6 characters long"})
		return
	}
	if err := accounts.UpdateUser(req.Uuid, req.Name, req.Password, req.SsoType); err != nil {
		c.JSON(500, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "success", "message": "User updated successfully", "uuid": req.Uuid})
}
