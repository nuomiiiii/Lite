package api

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/config"
)

func GetPublicSettings(c *gin.Context) {
	cst, err := config.Get()
	if err != nil {
		c.JSON(500, gin.H{
			"status": "error",
			"error":  "Internal Server Error: " + err.Error(),
		})
		return
	}
	// Return public settings including CORS
	c.JSON(200, gin.H{
		"status": "success",
		"data": gin.H{
			"sitename":               cst.Sitename,
			"description":            cst.Description,
			"custom_head":            cst.CustomHead,
			"custom_body":            cst.CustomBody,
			"oauth_enable":           cst.OAuthEnabled,
			"disable_password_login": cst.DisablePasswordLogin,
			"allow_cors":             cst.AllowCors,
		},
	})
}
