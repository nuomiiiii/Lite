package geoip_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nuomiiiii/lite/utils/geoip"
)

// 测试GeoIP数据库的初始化和更新功能
func TestMmdb(t *testing.T) {
	geoip.CurrentProvider, _ = geoip.NewMaxMindGeoIPService()
	testIpAddr(t)
}
func TestIpApi(t *testing.T) {
	geoip.CurrentProvider, _ = geoip.NewIPAPIService()
	testIpAddr(t)
}

func TestGeojs(t *testing.T) {
	geoip.CurrentProvider, _ = geoip.NewGeoJSService()
	testIpAddr(t)
}

func TestIpInfo(t *testing.T) {
	geoip.CurrentProvider, _ = geoip.NewIPInfoService()
	testIpAddr(t)
}
func testIpAddr(t *testing.T) {
	// IPv4
	ipaddr := "8.8.8.8"
	ip := net.ParseIP(ipaddr)
	record, err := geoip.GetGeoInfo(ip)
	if err != nil {
		t.Errorf("Failed to get GeoIP info for IP %s: %v", ipaddr, err)
	}

	if record != nil {
		if record.ISOCode == "" && record.Name == "" {
			t.Errorf("Country information is missing for IP %s", ipaddr)
		}
	} else {
		t.Errorf("GeoIP record is nil for IP %s", ipaddr)
	}

	t.Logf("IPv4:[%s]%s - %s", ipaddr, record.ISOCode, record.Name)

	// IPv6
	ipaddr = "2001:4860:4860::8888"
	ip = net.ParseIP(ipaddr)
	record, err = geoip.GetGeoInfo(ip)
	if err != nil {
		t.Errorf("Failed to get GeoIP info for IPv6 %s: %v", ipaddr, err)
	}
	if record != nil {
		if record.ISOCode == "" && record.Name == "" {
			t.Errorf("Country information is missing for IPv6 %s", ipaddr)
		}
	} else {
		t.Errorf("GeoIP record is nil for IPv6 %s", ipaddr)
	}
	t.Logf("IPv6:[%s]%s - %s", ipaddr, record.ISOCode, record.Name)
}

func TestUnicodeEmoji(t *testing.T) {
	ISOCode := "CN"
	emoji := geoip.GetRegionUnicodeEmoji(ISOCode)
	if emoji != "🇨🇳" {
		t.Errorf("Expected emoji for %s, got %s", ISOCode, emoji)
	}
	t.Logf("Emoji for %s: %s", ISOCode, emoji)
}

func TestMmdbUpdateDatabaseDoesNotLeakLockAndSwapsAtomically(t *testing.T) {
	mmdbBytes, err := os.ReadFile("./data/GeoLite2-Country.mmdb")
	if err != nil {
		t.Skipf("local test mmdb not present: %v", err)
	}

	origPath := geoip.GeoIpFilePath
	origURL := geoip.GeoIpUrl
	t.Cleanup(func() {
		geoip.GeoIpFilePath = origPath
		geoip.GeoIpUrl = origURL
	})

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "GeoLite2-Country.mmdb")
	if err := os.WriteFile(dbPath, mmdbBytes, 0o644); err != nil {
		t.Fatalf("seed mmdb: %v", err)
	}
	geoip.GeoIpFilePath = dbPath

	svc, err := geoip.NewMaxMindGeoIPService()
	if err != nil {
		t.Fatalf("NewMaxMindGeoIPService: %v", err)
	}
	defer svc.Close()

	failMode := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failMode {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mmdbBytes)
	}))
	defer server.Close()
	geoip.GeoIpUrl = server.URL

	// Error path must not leak write lock or break existing reader.
	if err := svc.UpdateDatabase(); err == nil {
		t.Fatalf("expected UpdateDatabase HTTP 500 error")
	}
	if rec, err := svc.GetGeoInfo(net.ParseIP("8.8.8.8")); err != nil || rec == nil {
		t.Fatalf("GetGeoInfo failed or deadlocked after failed UpdateDatabase: rec=%v err=%v", rec, err)
	}

	// Success path must validate temp file, atomically swap, and keep reader working.
	failMode = false
	if err := svc.UpdateDatabase(); err != nil {
		t.Fatalf("UpdateDatabase success path failed: %v", err)
	}
	if _, err := os.Stat(dbPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary mmdb file remained after swap: %v", err)
	}
	if rec, err := svc.GetGeoInfo(net.ParseIP("8.8.8.8")); err != nil || rec == nil || rec.ISOCode == "" {
		t.Fatalf("GetGeoInfo after atomic UpdateDatabase failed: rec=%v err=%v", rec, err)
	}
}
