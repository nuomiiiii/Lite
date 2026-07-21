package client

import (
	"net"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/utils/geoip"

	"github.com/gin-gonic/gin"
)

func getClientIPType(ip net.IP) int {
	// 0:ipv4 1:ipv6 -1:错误的输入
	if ip == nil {
		return -1
	}
	if ip.To4() == nil {
		return 1
	} else {
		return 0
	}
}

func saveClientBasicInfo(info map[string]interface{}, uuid string, fallbackIP string) error {
	monthRotate, reportsMonthRotate := info["month_rotate"]
	delete(info, "month_rotate")
	info["uuid"] = uuid
	applyFallbackClientIP(info, fallbackIP)
	appendClientRegionFromGeoIP(info)
	if err := clients.SaveClientInfo(info); err != nil {
		return err
	}
	if reportsMonthRotate {
		return clients.AdoptTrafficResetDay(uuid, monthRotate)
	}
	return nil
}

func applyFallbackClientIP(info map[string]interface{}, fallbackIP string) {
	if hasClientIP(info) {
		return
	}
	ip := net.ParseIP(fallbackIP)

	switch getClientIPType(ip) {
	case 0:
		info["ipv4"] = fallbackIP
	case 1:
		info["ipv6"] = fallbackIP
	}
}

func hasClientIP(info map[string]interface{}) bool {
	if ipv4, ok := info["ipv4"].(string); ok && ipv4 != "" {
		return true
	}
	if ipv6, ok := info["ipv6"].(string); ok && ipv6 != "" {
		return true
	}
	return false
}

func appendClientRegionFromGeoIP(info map[string]interface{}) {
	cfg, err := config.GetAs[bool](config.GeoIpEnabledKey)
	if err != nil || !cfg {
		return
	}

	for _, key := range []string{"ipv4", "ipv6"} {
		ipStr, ok := info[key].(string)
		if !ok || ipStr == "" {
			continue
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		record, _ := geoip.GetGeoInfo(ip)
		if record == nil {
			continue
		}
		region := geoip.GetRegionUnicodeEmoji(record.ISOCode)
		if region == "" {
			continue
		}
		info["region"] = region
		return
	}
}

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

	if err := ingestBasicInfo(uuid, cbi, c.ClientIP()); err != nil {
		c.JSON(500, gin.H{"status": "error", "error": err})
		return
	}

	response := gin.H{"status": "success"}
	runtimeConfig, err := getClientRuntimeConfig(uuid)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "error": err.Error()})
		return
	}
	if runtimeConfig != nil {
		response["config"] = runtimeConfig
	} else {
		response["request_config_state"] = true
	}
	c.JSON(200, response)
}
