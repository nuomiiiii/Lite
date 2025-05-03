package geoip_test

import (
	"net"
	"os"
	"testing"

	"github.com/komari-monitor/komari/utils/geoip"
)

// 测试GeoIP数据库的初始化和更新功能
func TestInitGeoIp(t *testing.T) {
	geoip.InitGeoIp()

	// 检查数据库
	fileInfo, err := os.Stat(geoip.GeoIpFilePath)
	if err != nil {
		t.Errorf("Failed to get file info: %v", err)
	}
	if fileInfo.Size() == 0 {
		t.Errorf("GeoIP database file is empty: %s", geoip.GeoIpFilePath)
	}

	// IPv4
	ipaddr := "8.8.8.8"
	ip := net.ParseIP(ipaddr)
	record, err := geoip.GetGeoIpInfo(ip)
	if err != nil {
		t.Errorf("Failed to get GeoIP info for IP %s: %v", ipaddr, err)
	}

	if record != nil {
		if record.Country.ISOCode == "" && record.Country.Names["zh-CN"] == "" {
			t.Errorf("Country information is missing for IP %s", ipaddr)
		}
	} else {
		t.Errorf("GeoIP record is nil for IP %s", ipaddr)
	}

	t.Logf("IPv4:[%s]%s - %s", ipaddr, record.Country.ISOCode, record.Country.Names["zh-CN"])

	// IPv6
	ipaddr = "2001:4860:4860::8888"
	ip = net.ParseIP(ipaddr)
	record, err = geoip.GetGeoIpInfo(ip)
	if err != nil {
		t.Errorf("Failed to get GeoIP info for IPv6 %s: %v", ipaddr, err)
	}
	if record != nil {
		if record.Country.ISOCode == "" && record.Country.Names["zh-CN"] == "" {
			t.Errorf("Country information is missing for IPv6 %s", ipaddr)
		}
	} else {
		t.Errorf("GeoIP record is nil for IPv6 %s", ipaddr)
	}
	t.Logf("IPv6:[%s]%s - %s", ipaddr, record.Country.ISOCode, record.Country.Names["zh-CN"])
}

func TestUnicodeEmoji(t *testing.T) {
	ISOCode := "CN"
	emoji := geoip.GetCountryUnicodeEmoji(ISOCode)
	if emoji != "🇨🇳" {
		t.Errorf("Expected emoji for %s, got %s", ISOCode, emoji)
	}
	t.Logf("Emoji for %s: %s", ISOCode, emoji)
}
