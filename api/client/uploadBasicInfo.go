package client

import (
	"net"

	"github.com/komari-monitor/komari/common"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/utils/geoip"

	"github.com/gin-gonic/gin"
)

func UploadBasicInfo(c *gin.Context) {
	var cbi = common.ClientInfo{}
	if err := c.ShouldBindJSON(&cbi); err != nil {
		c.JSON(400, gin.H{"status": "error", "error": "Invalid or missing data"})
		return
	}

	token := c.Query("token")
	uuid, err := clients.GetClientUUIDByToken(token)
	if uuid == "" || err != nil {
		c.JSON(400, gin.H{"status": "error", "error": "Invalid token"})
		return
	}

	cbi.UUID = uuid

	if cfg, err := config.Get(); err != nil && cfg.GeoIpEnabled {
		if cbi.IPv4 != "" {
			ip4 := net.ParseIP(cbi.IPv4)
			ip4_record, _ := geoip.GetGeoIpInfo(ip4)
			if ip4_record != nil {
				cbi.Country = geoip.GetCountryUnicodeEmoji(ip4_record.Country.ISOCode)
			}
		} else if cbi.IPv6 != "" {
			ip6 := net.ParseIP(cbi.IPv6)
			ip6_record, _ := geoip.GetGeoIpInfo(ip6)
			if ip6_record != nil {
				cbi.Country = geoip.GetCountryUnicodeEmoji(ip6_record.Country.ISOCode)
			}
		}
	}

	if err := clients.UpdateOrInsertBasicInfo(cbi); err != nil {
		c.JSON(500, gin.H{"status": "error", "error": err})
		return
	}

	c.JSON(200, gin.H{"status": "success"})
}
