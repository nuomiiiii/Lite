package tasks

import (
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

func getPingTasksByClient(db *gorm.DB, uuid string) ([]models.PingTask, error) {
	var tasks []models.PingTask
	if err := db.Where("clients LIKE ?", `%"`+uuid+`"%`).Order("weight ASC").Order("id ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}
