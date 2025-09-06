package api

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database"
)

func GetPublicSettings(c *gin.Context) {
	p, e := database.GetPublicInfo()
	if e != nil {
		RespondError(c, 500, e.Error())
		return
	}
	RespondSuccess(c, p)
}
