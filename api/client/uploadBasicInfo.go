package client

import (
	"net"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/utils/geoip"

	"github.com/gin-gonic/gin"
)

func UploadBasicInfo(c *gin.Context) {
	var cbi = map[string]interface{}{}
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

	cbi["uuid"] = uuid

	if cfg, err := config.Get(); err == nil && cfg.GeoIpEnabled {
		if ipv4, ok := cbi["ipv4"].(string); ok && ipv4 != "" {
			ip4 := net.ParseIP(ipv4)
			ip4_record, _ := geoip.GetGeoInfo(ip4)
			if ip4_record != nil {
				cbi["region"] = geoip.GetRegionUnicodeEmoji(ip4_record.ISOCode)
			}
		} else if ipv6, ok := cbi["ipv6"].(string); ok && ipv6 != "" {
			ip6 := net.ParseIP(ipv6)
			ip6_record, _ := geoip.GetGeoInfo(ip6)
			if ip6_record != nil {
				cbi["region"] = geoip.GetRegionUnicodeEmoji(ip6_record.ISOCode)
			}
		}
	}

	if err := clients.SaveClientInfo(cbi); err != nil {
		c.JSON(500, gin.H{"status": "error", "error": err})
		return
	}

	c.JSON(200, gin.H{"status": "success"})
}
