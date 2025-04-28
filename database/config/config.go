package config

import (
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func Get() (models.Config, error) {
	db := dbcore.GetDBInstance()
	var config models.Config
	if err := db.First(&config).Error; err != nil {
		return config, err
	}
	return config, nil
}

func Save(cst models.Config) error {
	db := dbcore.GetDBInstance()
	var config models.Config
	// Only one records
	config.ID = 1
	if err := db.Save(&config).Error; err != nil {
		return err
	}
	return nil
}
