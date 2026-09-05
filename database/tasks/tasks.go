package tasks

import (
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

const (
	taskWriteChunkSize     = 400
	saveTaskResultAttempts = 3
)

var saveTaskResultsAttemptHook func(attempt int) error

func CreateTask(taskId string, clients []string, command string) error {
	db := dbcore.GetDBInstance()
	task := models.Task{
		TaskId:  taskId,
		Clients: models.StringArray(clients),
		Command: command,
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		if len(clients) == 0 {
			return nil
		}
		now := time.Now().UTC()
		taskResults := make([]models.TaskResult, len(clients))
		for i, client := range clients {
			taskResults[i] = models.TaskResult{
				TaskId:     taskId,
				Client:     client,
				Result:     "",
				ExitCode:   nil,
				FinishedAt: nil,
				CreatedAt:  now,
			}
		}
		return createTaskResults(tx, taskResults)
	})
}

func createTaskResults(db *gorm.DB, taskResults []models.TaskResult) error {
	for i := 0; i < len(taskResults); i += taskWriteChunkSize {
		end := i + taskWriteChunkSize
		if end > len(taskResults) {
			end = len(taskResults)
		}
		chunk := taskResults[i:end]
		if err := db.Create(&chunk).Error; err != nil {
			return err
		}
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
func SaveTaskResult(taskId, clientId, result string, exitCode int, timestamp time.Time) error {
	return SaveTaskResults(taskId, []string{clientId}, result, exitCode, timestamp)
}

func CancelUndeliveredTaskResult(taskId, clientId, result string) error {
	if taskId == "" || clientId == "" {
		return nil
	}
	now := time.Now().UTC()
	exitCode := -1
	updates := map[string]interface{}{
		"result":      truncateTaskResult(result),
		"exit_code":   exitCode,
		"finished_at": now,
	}
	return dbcore.GetDBInstance().
		Model(&models.TaskResult{}).
		Where("task_id = ? AND client = ? AND finished_at IS NULL", taskId, clientId).
		Updates(updates).Error
}

func SaveIncomingTaskResult(taskId, clientId, result, status string, exitCode int, timestamp time.Time) error {
	updates := map[string]interface{}{
		"result":      truncateTaskResult(result),
		"exit_code":   exitCode,
		"finished_at": timestamp.UTC(),
	}
	db := dbcore.GetDBInstance()
	var last error
	for attempt := 1; attempt <= saveTaskResultAttempts; attempt++ {
		if saveTaskResultsAttemptHook != nil {
			if err := saveTaskResultsAttemptHook(attempt); err != nil {
				last = err
				continue
			}
		}
		query := db.Model(&models.TaskResult{}).Where("task_id = ? AND client = ?", taskId, clientId)
		if isWeakIncomingTaskResult(result, status) {
			query = query.Where("finished_at IS NULL OR result = '' OR result = ?", "execution status unknown")
		}
		op := query.Updates(updates)
		if op.Error != nil {
			last = op.Error
			continue
		}
		if op.RowsAffected > 0 {
			return nil
		}
		var count int64
		if err := db.Model(&models.TaskResult{}).Where("task_id = ? AND client = ?", taskId, clientId).Count(&count).Error; err != nil {
			last = err
			continue
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}
	return last
}

func isWeakIncomingTaskResult(result, status string) bool {
	if status == "interrupted" {
		return true
	}
	return result == "execution status unknown"
}

func isStrongStoredTaskResult(existing *models.TaskResult) bool {
	if existing == nil || existing.FinishedAt == nil {
		return false
	}
	if existing.Result == "" || existing.Result == "execution status unknown" {
		return false
	}
	return true
}

func shouldKeepExistingTaskResult(existing *models.TaskResult, result, status string) bool {
	return isStrongStoredTaskResult(existing) && isWeakIncomingTaskResult(result, status)
}

func SaveTaskResults(taskId string, clientIDs []string, result string, exitCode int, timestamp time.Time) error {
	if len(clientIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(clientIDs))
	unique := make([]string, 0, len(clientIDs))
	for _, clientID := range clientIDs {
		if clientID == "" {
			continue
		}
		if _, ok := seen[clientID]; ok {
			continue
		}
		seen[clientID] = struct{}{}
		unique = append(unique, clientID)
	}
	if len(unique) == 0 {
		return nil
	}
	updates := map[string]interface{}{
		"result":      truncateTaskResult(result),
		"exit_code":   exitCode,
		"finished_at": timestamp.UTC(),
	}
	db := dbcore.GetDBInstance()
	var last error
	for attempt := 1; attempt <= saveTaskResultAttempts; attempt++ {
		if saveTaskResultsAttemptHook != nil {
			if err := saveTaskResultsAttemptHook(attempt); err != nil {
				last = err
				continue
			}
		}
		last = saveTaskResultsOnce(db, taskId, unique, updates)
		if last == nil {
			return nil
		}
	}
	return last
}

func saveTaskResultsOnce(db *gorm.DB, taskId string, unique []string, updates map[string]interface{}) error {
	for i := 0; i < len(unique); i += taskWriteChunkSize {
		end := i + taskWriteChunkSize
		if end > len(unique) {
			end = len(unique)
		}
		if err := db.Model(&models.TaskResult{}).
			Where("task_id = ? AND client IN ?", taskId, unique[i:end]).
			Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

const maxTaskResultBytes = 1 << 20

func truncateTaskResult(result string) string {
	if len(result) <= maxTaskResultBytes {
		return result
	}
	truncated := result[:maxTaskResultBytes]
	for len(truncated) > 0 && truncated[len(truncated)-1]&0xc0 == 0x80 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "\n[truncated]"
}

func ClearTaskResultsByTimeBefore(before time.Time) error {
	return dbcore.GetDBInstance().Where("created_at < ?", before.UTC()).Delete(&models.TaskResult{}).Error
}
