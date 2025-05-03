package geoip

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/oschwald/maxminddb-golang"
)

var (
	GeoIpUrl                        = "https://gh-proxy.com/raw.githubusercontent.com/Loyalsoldier/geoip/release/GeoLite2-Country.mmdb"
	GeoIpFilePath                   = "./data/GeoLite2-Country.mmdb"
	geoIpDb       *maxminddb.Reader = nil
	lock                            = &sync.RWMutex{}
)

type GeoIpRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
}

// 更新Geoip数据库，使用 GeoIpUrl下载最新的数据库文件，并覆盖本地的 GeoIpFilePath 文件
func UpdateGeoIpDatabase() error {
	lock.Lock()
	defer lock.Unlock()
	if geoIpDb != nil {
		geoIpDb.Close()
		geoIpDb = nil
	}
	log.Println("Downloading GeoIP database...")
	resp, err := http.Get(GeoIpUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download file: %s", resp.Status)
	}

	out, err := os.Create(GeoIpFilePath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}
	return nil
}

func InitGeoIp() {
	if os.MkdirAll("./data", os.ModePerm) != nil {
		return
	}
	if _, err := os.Stat(GeoIpFilePath); os.IsNotExist(err) {
		err := UpdateGeoIpDatabase()
		if err != nil {
			log.Println("Error updating GeoIP database:", err)
		} else {
			log.Println("GeoIP database updated successfully.")
		}
	}
	var err error
	geoIpDb, err = maxminddb.Open(GeoIpFilePath)
	if err != nil {
		log.Printf("Error opening GeoIP database: %v", err)
	}
}

func GetGeoIpInfo(ip net.IP) (*GeoIpRecord, error) {
	lock.RLock()
	defer lock.RUnlock()
	if geoIpDb == nil {
		InitGeoIp()
	}
	if ip == nil {
		return nil, fmt.Errorf("IP address is nil")
	}
	var record GeoIpRecord
	err := geoIpDb.Lookup(ip, &record)
	if err != nil {
		log.Printf("Error looking up IP %s: %v", ip.String(), err)
		return nil, err
	}
	return &record, nil
}

func GetCountryUnicodeEmoji(isoCode string) string {
	if len(isoCode) != 2 {
		return ""
	}
	isoCode = strings.ToUpper(isoCode)

	if !unicode.IsLetter(rune(isoCode[0])) || !unicode.IsLetter(rune(isoCode[1])) {
		return ""
	}

	rune1 := rune(0x1F1E6 + (rune(isoCode[0]) - 'A'))
	rune2 := rune(0x1F1E6 + (rune(isoCode[1]) - 'A'))
	return string(rune1) + string(rune2)
}
