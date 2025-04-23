package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/history"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

func AddClient(c *gin.Context) {
	var req struct {
		ClientName string `json:"client_name" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	// 必须有名字
	if req.ClientName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Client name is required"})
		return
	}

	client, err := clients.CreateClient(req.ClientName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "detail": client})
}

func EditClient(c *gin.Context) {
	var req struct {
		UUID       string `json:"uuid" binding:"required"`
		ClientName string `json:"client_name,omitempty"`
		Token      string `json:"token,omitempty"`
		CpuName    string `json:"cpu_name,omitempty"`
		CpuCores   uint   `json:"cpu_cores,omitempty"`
		GpuName    string `json:"gpu_name,omitempty"`
		Os         string `json:"os"`
		Memory     int64  `json:"mem"`
		IPv4       string `json:"ipv4"`
		IPv6       string `json:"ipv6"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	var updates map[string]interface{}
	if req.ClientName != "" {
		updates = map[string]interface{}{"client_name": req.ClientName}
	}

	if req.Token != "" {
		updates = map[string]interface{}{"token": req.Token}
	}

	if req.CpuName != "" {
		updates = map[string]interface{}{"cpu_name": req.CpuName}
	}
	if req.CpuCores != 0 {
		updates = map[string]interface{}{"cpu_cores": req.CpuCores}
	}
	if req.Memory != 0 {
		updates = map[string]interface{}{"memory": req.Memory}
	}
	if req.IPv4 != "" {
		updates = map[string]interface{}{"ipv4": req.IPv4}
	}
	if req.IPv6 != "" {
		updates = map[string]interface{}{"ipv6": req.IPv6}
	}
	db := dbcore.GetDBInstance()
	if len(updates) > 0 {
		err := db.Model(&models.Client{}).Updates(updates).Error
		if err != nil {
			c.JSON(500, gin.H{"status": "error", "error": err})
			return
		}
	} else {
		c.JSON(400, gin.H{"status": "error", "error": "No updates provided"})
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

	c.JSON(200, gin.H{"status": "success"})
}

func ClearHistory(c *gin.Context) {
	if err := history.DeleteAll(); err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Failed to delete history" + err.Error(),
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
	if err == gorm.ErrRecordNotFound {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
	} else if err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  err,
		})
	}

	c.JSON(http.StatusOK, result)
}

func ListClients(c *gin.Context) {
	cls, err := clients.GetAllClients()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cls)
}
