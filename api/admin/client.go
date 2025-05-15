package admin

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/records"
)

func AddClient(c *gin.Context) {
	var req struct {
		name string
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.name == "" {
		uuid, token, err := clients.CreateClient()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "uuid": uuid, "token": token})
		return
	}
	uuid, token, err := clients.CreateClientWithName(req.name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "uuid": uuid, "token": token})
}

func EditClient(c *gin.Context) {
	var req = make(map[string]interface{})
	uuid := c.Param("uuid")
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}
	db := dbcore.GetDBInstance()
	var err error
	if req["token"] != "" {
		err = db.Model(&models.Client{}).Where("uuid = ?", uuid).
			Updates(map[string]interface{}{"token": req["token"], "updated_at": time.Now()}).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
	}
	allowed_fields := []string{"name", "remark", "weight", "price", "expired_at"}
	updateFields := map[string]interface{}{
		"updated_at": time.Now(),
	}
	for _, field := range allowed_fields {
		if req[field] != nil {
			updateFields[field] = req[field]
		}
	}
	if len(updateFields) > 1 { // 大于1是因为至少包含了updated_at
		if err := db.Model(&common.ClientInfo{}).Where("uuid = ?", uuid).Updates(updateFields).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func RemoveClient(c *gin.Context) {
	uuid := c.Param("uuid")
	err := clients.DeleteClient(uuid)
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
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
		return
	}

	result, err := clients.GetClientByUUID(uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"status": "error",
			"error":  err.Error(),
		})
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

func GetClientToken(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
		return
	}

	token, err := clients.GetClientTokenByUUID(uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "token": token})
}
