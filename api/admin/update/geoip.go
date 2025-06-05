package update

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/utils/geoip"
)

func UpdateMmdbGeoIP(c *gin.Context) {
	if err := geoip.UpdateGeoIpDatabase(); err != nil {
		api.RespondError(c, 500, "Failed to update GeoIP database "+err.Error())
		return
	}
	api.RespondSuccess(c, nil)
}
