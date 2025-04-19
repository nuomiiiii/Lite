package api

import (
	"github.com/akizon77/komari/database/accounts"

	"github.com/gin-gonic/gin"
)

func GetMe(c *gin.Context) {
	session, err := c.Cookie("session_token")
	if err != nil {
		c.JSON(200, gin.H{"username": "Guest"})
		return
	}
	uuid, err := accounts.GetSession(session)
	if err != nil {
		c.JSON(200, gin.H{"username": "Guest"})
		return
	}
	user, err := accounts.GetUserByUUID(uuid)
	if err != nil {
		c.JSON(200, gin.H{"username": "Guest"})
		return
	}
	c.JSON(200, gin.H{"username": user.Username})

}
