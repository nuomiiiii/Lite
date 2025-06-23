package api

import (
	"github.com/gin-gonic/gin"
)

func GetClientRecentRecords(c *gin.Context) {
	uuid := c.Param("uuid")
	if uuid == "" {
		RespondError(c, 400, "UUID is required")
		return
	}
	records, _ := Records.Get(uuid)
	RespondSuccess(c, records)
}
