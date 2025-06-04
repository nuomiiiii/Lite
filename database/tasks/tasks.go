package tasks

import (
	"encoding/json"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func CreateTask(taskId string, clients []string, command string) error {
	db := dbcore.GetDBInstance()
	// Convert clients slice to JSON string
	clientsJSON, err := json.Marshal(clients)
	if err != nil {
		return err
	}
	// Create a new task in the database
	task := models.Task{
		TaskId:  taskId,
		Clients: string(clientsJSON),
		Command: command,
	}
	if err := db.Create(&task).Error; err != nil {
		return err
	}
	var taskResults []models.TaskResult
	for _, client := range clients {
		taskResults = append(taskResults, models.TaskResult{
			TaskId:     taskId,
			Client:     client,
			Result:     "",
			ExitCode:   nil,
			FinishedAt: "",
			CreatedAt:  time.Now().Format(time.RFC3339),
		})
	}
	if len(taskResults) > 0 {
		return db.Create(&taskResults).Error
	}
	return nil
}
func GetTaskByTaskId(taskId string) (*models.Task, error) {
	var task models.Task
	if err := dbcore.GetDBInstance().Where("task_id = ?", taskId).First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}
func GetTasksByClientId(clientId string) ([]models.Task, error) {
	var tasks []models.Task
	if err := dbcore.GetDBInstance().Where("clients LIKE ?", "%"+clientId+"%").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func GetSpecificTaskResult(taskId, clientId string) (*models.TaskResult, error) {
	var result models.TaskResult
	if err := dbcore.GetDBInstance().Where("task_id = ? AND client = ?", taskId, clientId).First(&result).Error; err != nil {
		return nil, err
	}
	return &result, nil
}

func GetAllTasksResultByUUID(uuid string) ([]models.TaskResult, error) {
	var results []models.TaskResult
	if err := dbcore.GetDBInstance().Where("client = ?", uuid).Find(&results).Error; err != nil {
		return nil, err
	}
	return results, nil
}
func GetAllTasks() ([]models.Task, error) {
	var tasks []models.Task
	if err := dbcore.GetDBInstance().Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func GetTaskResultsByTaskId(taskId string) ([]models.TaskResult, error) {
	var results []models.TaskResult
	if err := dbcore.GetDBInstance().Where("task_id = ?", taskId).Find(&results).Error; err != nil {
		return nil, err
	}
	return results, nil
}
func DeleteTaskByTaskId(taskId string) error {
	return dbcore.GetDBInstance().Where("task_id = ?", taskId).Delete(&models.Task{}).Error
}

func SaveTaskResult(taskId, clientId, result string, exitCode int, timestamp time.Time) error {
	taskResult := models.TaskResult{
		TaskId:     taskId,
		Client:     clientId,
		Result:     result,
		ExitCode:   &exitCode,
		FinishedAt: timestamp.Format(time.RFC3339),
	}
	return dbcore.GetDBInstance().Create(&taskResult).Error
}

func ClearTaskResultsByTimeBefore(before time.Time) error {
	return dbcore.GetDBInstance().Where("created_at < ?", before.Format(time.RFC3339)).Delete(&models.TaskResult{}).Error
}
