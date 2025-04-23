package client

import (
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"gorm.io/gorm"

	"github.com/gin-gonic/gin"
)

func UploadBasicInfo(c *gin.Context) {
	uuid := c.Query("uuid")
	if uuid == "" {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
		return
	}

	client, err := clients.GetClientByUUID(uuid)
	if err == gorm.ErrRecordNotFound {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid or missing UUID",
		})
	}

	var req struct {
		CpuName  string `gorm:"type:varchar(100)" json:"cpuName;omitempty"`
		CpuCores uint   `json:"cpuCores;omitempty"`
		GpuName  string `gorm:"type:varchar(100)" json:"gpuName;omitempty"`
		Os       string `gorm:"type:varchar(100)" json:"os;omitempty"`
		Memory   int64  `gorm:"type:bigint" json:"memory;omitempty"`
		IPv4     string `gorm:"type:varchar(100)" json:"ipv4;omitempty"`
		IPv6     string `gorm:"type:varchar(100)" json:"ipv6;omitempty"`
	}

	err = c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(400, gin.H{
			"status": "error",
			"error":  "Invalid JSON data",
		})
	}
	db := dbcore.GetDBInstance()

	updates := map[string]interface{}{}
	if req.CpuName != "" {
		updates["cpu_name"] = req.CpuName
	}
	if req.CpuCores != 0 {
		updates["cpu_cores"] = req.CpuCores
	}
	if req.GpuName != "" {
		updates["gpu_name"] = req.GpuName
	}
	if req.Os != "" {
		updates["os"] = req.Os
	}
	if req.Memory != 0 {
		updates["memory"] = req.Memory
	}
	if req.IPv4 != "" {
		updates["ipv4"] = req.IPv4
	}
	if req.IPv6 != "" {
		updates["ipv6"] = req.IPv6
	}

	if len(updates) > 0 {
		err = db.Model(&client).Updates(updates).Error
		if err != nil {
			c.JSON(500, gin.H{"status": "error", "error": err})
		}
	} else {
		c.JSON(400, gin.H{"status": "error", "error": "No updates provided"})
	}
	c.JSON(200, gin.H{"status": "success"})
}
