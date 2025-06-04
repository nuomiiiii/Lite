package config

import (
	"log"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

func Get() (models.Config, error) {
	db := dbcore.GetDBInstance()
	var config models.Config
	if err := db.First(&config).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			config = models.Config{
				ID:            1,
				Sitename:      "Komari",
				Description:   "Komari Monitor, a simple server monitoring tool.",
				AllowCors:     false,
				OAuthEnabled:  false,
				GeoIpEnabled:  true,
				GeoIpProvider: "mmdb",
				UpdatedAt:     time.Now(),
				CreatedAt:     time.Now(),
			}
			if err := db.Create(&config).Error; err != nil {
				log.Fatal("Failed to create default config:", err)
			}
			return config, nil
		}
		return config, err
	}
	return config, nil
}

func Save(cst models.Config) error {
	db := dbcore.GetDBInstance()
	// Only one records
	cst.ID = 1
	cst.UpdatedAt = time.Now()
	// Do not update CreatedAt
	if err := db.Model(&models.Config{}).Where("id = ?", cst.ID).
		Select("sitename",
			"description",
			"allow_cros",
			"geo_ip_enabled",
			"geo_ip_provider",
			"o_auth_client_id",
			"o_auth_client_secret",
			"o_auth_enabled",
			"custom_head",
			"updated_at").
		Updates(cst).Error; err != nil {
		return err
	}
	return nil
}

func Update(cst map[string]interface{}) error {
	db := dbcore.GetDBInstance()

	// Proceed with update
	cst["id"] = 1
	cst["updated_at"] = time.Now()
	delete(cst, "created_at")
	delete(cst, "CreatedAt")
	// Map JSON key allow_cors to DB column allow_cros
	if v, ok := cst["allow_cors"]; ok {
		cst["allow_cros"] = v
		delete(cst, "allow_cors")
	}
	if err := db.Model(&models.Config{}).Where("id = ?", 1).Updates(cst).Error; err != nil {
		return err
	}
	return nil
}
