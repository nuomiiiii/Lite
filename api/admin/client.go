package admin

import (
	"net/http"
	"time"

	"github.com/akizon77/komari/common"
	"github.com/akizon77/komari/database/clients"
	"github.com/akizon77/komari/database/history"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func AddClient(c *gin.Context) {
	var config common.ClientConfig
	if err := c.ShouldBindJSON(&config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	uuid, token, err := clients.CreateClient(config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "uuid": uuid, "token": token})
}

func EditClient(c *gin.Context) {
	var req struct {
		UUID       string              `json:"uuid" binding:"required"`
		ClientName string              `json:"client_name,omitempty"`
		Token      string              `json:"token,omitempty"`
		Config     common.ClientConfig `json:"config,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": err.Error()})
		return
	}

	// 验证配置
	if req.Config.Interval <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Interval must be greater than 0"})
		return
	}

	// 获取原始配置
	rawConfig, err := clients.GetClientConfig(req.UUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}

	// 更新配置
	if req.Config.ClientUUID != "" {
		// 确保创建时间不变
		req.Config.CreatedAt = rawConfig.CreatedAt
		req.Config.UpdatedAt = time.Now()
		err = clients.UpdateClientConfig(req.Config)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
	}

	// 更新客户端名称
	if req.ClientName != "" {
		err = clients.EditClientName(req.UUID, req.ClientName)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
	}

	// 更新token
	if req.Token != "" {
		err = clients.EditClientToken(req.UUID, req.Token)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
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

	result := getClientByUUID(uuid)
	if result == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "Failed to get client"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func getClientByUUID(uuid string) map[string]interface{} {
	clientBasicInfo, err := clients.GetClientBasicInfo(uuid)
	if err == gorm.ErrRecordNotFound {
		clientBasicInfo = clients.ClientBasicInfo{}
	}
	config, err := clients.GetClientConfig(uuid)
	if err != nil {
		return nil
	}
	client, err := clients.GetClientByUUID(uuid)
	if err != nil {
		return nil
	}
	result := map[string]interface{}{
		"uuid":   uuid,
		"token":  client.Token,
		"info":   clientBasicInfo,
		"config": config,
	}
	return result
}

func getClientInfo(uuid string) (map[string]interface{}, error) {
	clientBasicInfo, err := clients.GetClientBasicInfo(uuid)
	if err == gorm.ErrRecordNotFound {
		clientBasicInfo = clients.ClientBasicInfo{}
	}
	config, err := clients.GetClientConfig(uuid)
	if err != nil {
		return nil, err
	}
	client, err := clients.GetClientByUUID(uuid)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"uuid":   uuid,
		"token":  client.Token,
		"name":   client.ClientName,
		"info":   clientBasicInfo,
		"config": config,
	}, nil
}

func ListClients(c *gin.Context) {
	cls, err := clients.GetAllClients()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
		return
	}
	result := []map[string]interface{}{}
	for i := range cls {
		clientInfo, err := getClientInfo(cls[i].UUID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": err.Error()})
			return
		}
		result = append(result, clientInfo)
	}
	c.JSON(http.StatusOK, result)
}
