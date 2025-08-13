package api

import (
	"encoding/json"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func GetPublicSettings(c *gin.Context) {
	cst, err := config.Get()
	if err != nil {
		RespondError(c, 500, err.Error())
		return
	}
	db := dbcore.GetDBInstance()
	tc := models.ThemeConfiguration{}
	err = db.Model(&models.ThemeConfiguration{}).Where("short = ?", cst.Theme).First(&tc).Error
	if err != nil {
		tc.Data = "{}"
	}
	tc_data := gin.H{}
	err = json.Unmarshal([]byte(tc.Data), &tc_data)
	if err != nil {
		log.Printf("%v", err)
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
		"theme":                     cst.Theme,
		"theme_settings":            tc_data,
	})

}
