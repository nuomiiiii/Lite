package api

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/utils"
)

func GetVersion(c *gin.Context) {
	RespondSuccess(c, gin.H{
		"version": utils.CurrentVersion,
		"hash":    utils.VersionHash,
		"status":  "success",
	})
}
