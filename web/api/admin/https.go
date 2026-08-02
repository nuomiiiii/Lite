package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/utils/httpsserver"
	"github.com/komari-monitor/komari/web/api"
)

func GetHTTPSSettings(c *gin.Context) {
	settings, err := httpsserver.LoadSettings()
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to load HTTPS settings: "+err.Error())
		return
	}
	api.RespondSuccess(c, gin.H{"settings": settings, "status": httpsserver.Default.Status()})
}

func UpdateHTTPSSettings(c *gin.Context) {
	var requested httpsserver.Settings
	if err := c.ShouldBindJSON(&requested); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid HTTPS settings: "+err.Error())
		return
	}
	normalized, err := httpsserver.Normalize(requested)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	previous, err := httpsserver.LoadSettings()
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, "Failed to load current HTTPS settings: "+err.Error())
		return
	}
	if normalized.Enabled {
		if err := httpsserver.Default.Apply(normalized); err != nil {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		if err := config.SetMany(normalized.ConfigMap()); err != nil {
			_ = httpsserver.Default.Apply(previous)
			api.RespondError(c, http.StatusInternalServerError, "Failed to save HTTPS settings: "+err.Error())
			return
		}
	} else {
		if err := config.SetMany(normalized.ConfigMap()); err != nil {
			api.RespondError(c, http.StatusInternalServerError, "Failed to save HTTPS settings: "+err.Error())
			return
		}
		if err := httpsserver.Default.DisableAfterResponse(normalized, time.Second); err != nil {
			_ = config.SetMany(previous.ConfigMap())
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.Header("Strict-Transport-Security", "max-age=0")
		c.Header("Connection", "close")
	}
	uuid, _ := c.Get("uuid")
	auditlog.Log(c.ClientIP(), fmt.Sprint(uuid), "updated built-in HTTPS settings", "warn")
	api.RespondSuccess(c, gin.H{
		"settings":     normalized,
		"status":       httpsserver.Default.Status(),
		"http_origin":  httpsserver.Default.HTTPOrigin(c.Request),
		"https_origin": httpsserver.Default.HTTPSOrigin(c.Request),
	})
}

func ReloadHTTPSCertificate(c *gin.Context) {
	if err := httpsserver.Default.ReloadCertificate(); err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	uuid, _ := c.Get("uuid")
	auditlog.Log(c.ClientIP(), fmt.Sprint(uuid), "reloaded built-in HTTPS certificate", "info")
	api.RespondSuccess(c, gin.H{"status": httpsserver.Default.Status()})
}
