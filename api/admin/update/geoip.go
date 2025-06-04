package update

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/utils/geoip"
)

func UpdateMmdbGeoIP(c *gin.Context) {
	if err := geoip.UpdateGeoIpDatabase(); err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to update GeoIP database: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "success", "message": "GeoIP database updated successfully"})
}
