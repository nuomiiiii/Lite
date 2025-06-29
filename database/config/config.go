package config

import (
	"errors"
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
				GeoIpProvider: "ip-api",
				EmailPort:     587,
				EmailUseSSL:   true,
				EmailReceiver: "", // Default empty
				UpdatedAt:     models.FromTime(time.Now()),
				CreatedAt:     models.FromTime(time.Now()),
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
	oldConfig, _ := Get()
	// Only one records
	cst.ID = 1
	cst.UpdatedAt = models.FromTime(time.Now())
	// Do not update CreatedAt
	if err := db.Model(&models.Config{}).Where("id = ?", cst.ID).
		Select("sitename",
			"description",
			"allow_cors",
			"geo_ip_enabled",
			"geo_ip_provider",
			"o_auth_client_id",
			"o_auth_client_secret",
			"o_auth_enabled",
			"custom_head",
			"email_enabled",
			"email_host",
			"email_port",
			"email_username",
			"email_password",
			"email_sender",
			"email_use_ssl",
			"email_receiver",
			"updated_at").
		Updates(cst).Error; err != nil {
		return err
	}
	newConfig, _ := Get()
	publishEvent(oldConfig, newConfig)
	return nil
}

func Update(cst map[string]interface{}) error {
	db := dbcore.GetDBInstance()
	oldConfig, _ := Get()
	// Proceed with update
	cst["id"] = 1
	cst["updated_at"] = time.Now()
	delete(cst, "created_at")
	delete(cst, "CreatedAt")

	// 至少有一种登录方式启用
	newDisablePasswordLogin := oldConfig.DisablePasswordLogin
	newOAuthEnabled := oldConfig.OAuthEnabled
	if val, exists := cst["disable_password_login"]; exists {
		newDisablePasswordLogin = val.(bool)
	}
	if val, exists := cst["o_auth_enabled"]; exists {
		newOAuthEnabled = val.(bool)
	}
	if newDisablePasswordLogin && !newOAuthEnabled {
		return errors.New("at least one login method must be enabled (password/oauth)")
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Config{}).Where("id = ?", oldConfig.ID).Updates(cst).Error; err != nil {
			return errors.Join(err, errors.New("failed to update configuration"))
		}
		newConfig := &models.Config{}
		if err := tx.Where("id = ?", oldConfig.ID).First(newConfig).Error; err != nil {
			return errors.Join(err, errors.New("failed to retrieve updated configuration"))
		}
		publishEvent(oldConfig, *newConfig)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}
