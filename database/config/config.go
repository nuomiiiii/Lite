package config

import (
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/public"
	"gorm.io/gorm"
)

func Get() (models.Config, error) {
	db := dbcore.GetDBInstance()
	var config models.Config
	if err := db.First(&config).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			config = models.Config{
				ID:        1,
				UpdatedAt: time.Now(),
				CreatedAt: time.Now(),
			}
			Save(config)
			return config, nil
		}
		return config, err
	}
	return config, nil
}

func Save(cst models.Config) error {
	db := dbcore.GetDBInstance()
	var config models.Config
	// Only one records
	cst.ID = 1
	cst.UpdatedAt = time.Now()
	if err := db.Save(&cst).Error; err != nil {
		return err
	}
	go public.UpdateIndex(config)
	return nil
}
