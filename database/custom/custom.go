package custom

import (
	"github.com/akizon77/komari/database/dbcore"
	"github.com/akizon77/komari/database/models"
)

func Get() (models.Custom, error) {
	db := dbcore.GetDBInstance()
	var custom models.Custom
	if err := db.First(&custom).Error; err != nil {
		return custom, err
	}
	return custom, nil
}

func Save(cst models.Custom) error {
	db := dbcore.GetDBInstance()
	var custom models.Custom
	// Only one record
	custom.ID = 1
	if err := db.Save(&custom).Error; err != nil {
		return err
	}
	return nil
}
