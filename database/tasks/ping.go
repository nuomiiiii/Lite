package tasks

import (
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
	"gorm.io/gorm"
)

func AddPingTask(clients []string, target, task_type string, interval int) (uint, error) {
	db := dbcore.GetDBInstance()
	task := models.PingTask{
		Clients:  clients,
		Type:     task_type,
		Target:   target,
		Interval: interval,
	}
	if err := db.Create(&task).Error; err != nil {
		return 0, err
	}
	ReloadPingSchedule()
	return task.Id, nil
}

func DeletePingTask(id []uint) error {
	db := dbcore.GetDBInstance()
	result := db.Where("id IN ?", id).Delete(&models.PingTask{})
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	ReloadPingSchedule()
	return result.Error
}

func EditPingTask(id []uint, updates map[string]interface{}) error {
	db := dbcore.GetDBInstance()
	result := db.Model(&models.PingTask{}).Where("id IN ?", id).Updates(updates)
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	ReloadPingSchedule()
	return result.Error
}

func GetAllPingTasks() ([]models.PingTask, error) {
	db := dbcore.GetDBInstance()
	var tasks []models.PingTask
	if err := db.Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func SavePingRecord(record models.PingRecord) error {
	db := dbcore.GetDBInstance()
	return db.Create(&record).Error
}

func GetPingRecords(client string) ([]models.PingRecord, error) {
	db := dbcore.GetDBInstance()
	var records []models.PingRecord
	if err := db.Where("client = ?", client).Order("time DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

func DeletePingRecordsBefore(time time.Time) error {
	db := dbcore.GetDBInstance()
	err := db.Where("time < ?", time).Delete(&models.PingRecord{}).Error
	return err
}

func DeletePingRecords(id []uint) error {
	db := dbcore.GetDBInstance()
	result := db.Where("task_id IN ?", id).Delete(&models.PingRecord{})
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return result.Error
}

func ReloadPingSchedule() error {
	db := dbcore.GetDBInstance()
	var pingTasks []models.PingTask
	if err := db.Find(&pingTasks).Error; err != nil {
		return err
	}
	return utils.ReloadPingSchedule(pingTasks)
}
func GetPingRecordsByClientAndTime(uuid string, start, end time.Time) ([]models.PingRecord, error) {
	db := dbcore.GetDBInstance()
	var records []models.PingRecord
	if err := db.Where("client = ? AND time >= ? AND time <= ?", uuid, start, end).Order("time DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}
