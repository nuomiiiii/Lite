package history

import (
	"time"

	"github.com/akizon77/komari/database/dbcore"
	"github.com/akizon77/komari/database/models"
)

func RecordOne(rec models.History) error {
	db := dbcore.GetDBInstance()
	return db.Create(&rec).Error
}

func DeleteAll() error {
	db := dbcore.GetDBInstance()
	return db.Exec("DELETE FROM history").Error
}

func GetLatestHistory(uuid string) (history []models.History, err error) {
	db := dbcore.GetDBInstance()
	err = db.Where("ClientUUID = ?", uuid).Order("time DESC").Limit(1).Find(&history).Error
	return
}

func DeleteRecordBefore(before time.Time) error {
	db := dbcore.GetDBInstance()
	return db.Where("time < ?", before).Delete(&models.History{}).Error
}
