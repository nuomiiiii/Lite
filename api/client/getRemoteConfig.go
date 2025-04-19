package client

import (
	"fmt"
	"net/http"

	"github.com/akizon77/komari/database/clients"
	"gorm.io/gorm"

	"github.com/gin-gonic/gin"
)

func GetRemoteConfig(c *gin.Context) {
	token := c.Query("token")

	clientUUID, err := clients.GetClientUUIDByToken(token)
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "No data found"})
		return
	} else if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": fmt.Sprintf("%v", err)})
		return
	}
	config, err := clients.GetClientConfig(clientUUID)

	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "No data found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": fmt.Errorf("error querying client: %v", err)})
		return
	}

	c.JSON(http.StatusOK, config)

}
