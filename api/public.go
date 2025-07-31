package api

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/config"
)

func GetPublicSettings(c *gin.Context) {
	cst, err := config.Get()
	if err != nil {
		RespondError(c, 500, err.Error())
		return
	}
	// Return public settings including CORS
	RespondSuccess(c, gin.H{
		"sitename":                  cst.Sitename,
		"description":               cst.Description,
		"custom_head":               cst.CustomHead,
		"custom_body":               cst.CustomBody,
		"oauth_enable":              cst.OAuthEnabled,
		"oauth_provider":            cst.OAuthProvider,
		"disable_password_login":    cst.DisablePasswordLogin,
		"allow_cors":                cst.AllowCors,
		"record_enabled":            cst.RecordEnabled,
		"record_preserve_time":      cst.RecordPreserveTime,
		"ping_record_preserve_time": cst.PingRecordPreserveTime,
		"private_site":              cst.PrivateSite,
	})

}
