package admin

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/records"
)

func AddClient(c *gin.Context) {

	uuid, token, err := clients.CreateClient()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "uuid": uuid, "token": token})
}

func EditClient(c *gin.Context) {
	var req struct {
		UUID       string `json:"uuid" binding:"required"`
		ClientName string `json:"name,omitempty"`
		Token      string `json:"token,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	updates := make(map[string]interface{})
	updates["updated_at"] = time.Now()
	if req.ClientName != "" {
		updates["client_name"] = req.ClientName
	}
	if req.Token != "" {
		updates["token"] = req.Token
	}
	db := dbcore.GetDBInstance()
	if err := db.Model(&models.Client{}).Where("uuid = ?", req.UUID).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func RemoveClient(c *gin.Context) {
	var req struct {
		UUID string `json:"uuid" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
	}
	err := clients.DeleteClientConfig(req.UUID)
	if err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Failed to delete client" + err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{"status": "success"})
}

func ClearRecord(c *gin.Context) {
	if err := records.DeleteAll(); err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Failed to delete Record" + err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{"status": "success"})
}

func GetClient(c *gin.Context) {
	uuid := c.Query("uuid")
	if uuid == "" {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
		return
	}

	result, err := clients.GetClientByUUID(uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

func ListClients(c *gin.Context) {
	cls, err := clients.GetAllClientBasicInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, cls)
}
