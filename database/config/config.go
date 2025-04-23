package config

import (
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func Get() (models.Config, error) {
	db := dbcore.GetDBInstance()
	var custom models.Config
	if err := db.First(&custom).Error; err != nil {
		return custom, err
	}
	return custom, nil
}

func Save(cst models.Config) error {
	db := dbcore.GetDBInstance()
	var custom models.Config
	// Only one record
	custom.ID = 1
	if err := db.Save(&custom).Error; err != nil {
		return err
	}
	return nil
}
