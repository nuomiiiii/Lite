package log

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func GetLogs(c *gin.Context) {
	limit := c.Query("limit")
	if limit == "" {
		limit = "100" // Default to 100 logs if not specified
	}
	page := c.Query("page")
	if page == "" {
		page = "1" // Default to page 1 if not specified
	}
	// Convert limit and page to integers
	// If conversion fails, return an error
	limitInt, err := strconv.Atoi(limit)
	if err != nil || limitInt <= 0 {
		api.RespondError(c, 400, "Invalid limit parameter: "+err.Error())
		return
	}
	pageInt, err := strconv.Atoi(page)
	if err != nil || pageInt <= 0 {
		api.RespondError(c, 400, "Invalid page parameter: "+err.Error())
		return
	}
	db := dbcore.GetDBInstance()
	var logs []models.Log
	// 添加分页：计算偏移量并限制数量
	offset := (pageInt - 1) * limitInt
	if err := db.Order("time desc").Limit(limitInt).Offset(offset).Find(&logs).Error; err != nil {
		api.RespondError(c, 500, "Failed to retrieve logs: "+err.Error())
		return
	}
	api.RespondSuccess(c, logs)
}
