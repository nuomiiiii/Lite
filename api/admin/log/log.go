package log

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func GetLogs(c *gin.Context) {
	db := dbcore.GetDBInstance()
	var logs []models.Log
	if err := db.Order("time desc").Find(&logs).Error; err != nil {
		api.RespondError(c, 500, "Failed to retrieve logs: "+err.Error())
		return
	}
	api.RespondSuccess(c, logs)
}
