package tasks

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/notificationdefaults"
	"github.com/nuomiiiii/lite/utils"
	logger "github.com/nuomiiiii/lite/utils/log"
	"gorm.io/gorm"
)

// AddPingTask 创建延迟监测任务。defaultOn 表示新加入的服务器是否自动开启此监测。
func AddPingTask(clients []string, defaultOn bool, name string, target, task_type string, interval int) (uint, error) {
	taskID, err := addPingTask(dbcore.GetDBInstance(), clients, defaultOn, name, target, task_type, interval)
	if err != nil {
		return 0, err
	}
	ReloadPingSchedule()
	return taskID, nil
}

func addPingTask(db *gorm.DB, clients []string, defaultOn bool, name string, target, task_type string, interval int) (uint, error) {
	normalizedClients := normalizePingClients(models.StringArray(clients))
	task := models.PingTask{
		Clients:   normalizedClients,
		DefaultOn: defaultOn,
		Name:      name,
		Type:      task_type,
		Target:    target,
		Interval:  interval,
	}
	pingLossDefault, err := notificationdefaults.GetPingLossNotificationDefaultConfig()
	if err != nil {
		return 0, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&task).Error; err != nil {
			return err
		}

		// Append by id to avoid races between concurrent create requests.
		result := tx.Model(&models.PingTask{}).Where("id = ?", task.Id).Update("weight", int(task.Id))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return notificationdefaults.ApplyLoadedPingLossDefaultsToTaskClients(tx, pingLossDefault, task.Id, normalizedClients)
	})
	if err != nil {
		return 0, err
	}
	return task.Id, nil
}

func DeletePingTask(id []uint) error {
	if len(id) == 0 {
		return fmt.Errorf("ping task id is required")
	}
	metricstore.BlockPingTaskWrites(id)
	deleted := false
	defer func() {
		if !deleted {
			metricstore.UnblockPingTaskWrites(id)
		}
	}()
	db := dbcore.GetDBInstance()
	if err := deletePingTaskRows(db, id); err != nil {
		return err
	}
	deleted = true
	if err := metricstore.ProcessPendingCleanupJobs(context.Background(), db); err != nil {
		logger.Errorf("metricstore", "Ping tasks were deleted; metric cleanup remains queued: %v", err)
	}
	return ReloadPingSchedule()
}

func deletePingTaskRows(db *gorm.DB, ids []uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			if err := metricstore.EnqueuePingTaskCleanup(tx, id); err != nil {
				return fmt.Errorf("queue ping task %d metric cleanup: %w", id, err)
			}
		}
		if tx.Migrator().HasTable("ping_records") {
			if err := tx.Exec("DELETE FROM ping_records WHERE task_id IN ?", ids).Error; err != nil {
				return fmt.Errorf("delete legacy ping records: %w", err)
			}
		}
		if err := tx.Where("task_id IN ?", ids).Delete(&models.PingLossNotification{}).Error; err != nil {
			return err
		}

		result := tx.Where("id IN ?", ids).Delete(&models.PingTask{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// EditPingTask 批量更新延迟监测任务配置。
func EditPingTask(tasks []*models.PingTask) error {
	taskIDs := editedPingTaskIDs(tasks)
	metricstore.BlockPingTaskWrites(taskIDs)
	defer metricstore.UnblockPingTaskWrites(taskIDs)

	_, err := editPingTasks(dbcore.GetDBInstance(), tasks)
	if err != nil {
		return err
	}
	reloadErr := ReloadPingSchedule()
	if cleanupErr := metricstore.ProcessPendingCleanupJobs(context.Background(), dbcore.GetDBInstance()); cleanupErr != nil {
		logger.Errorf("metricstore", "Ping assignments were removed; metric cleanup remains queued: %v", cleanupErr)
	}
	return reloadErr
}

func editPingTasks(db *gorm.DB, tasks []*models.PingTask) ([]metricstore.PingAssignment, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("at least one ping task is required")
	}
	removedAssignments := make([]metricstore.PingAssignment, 0)
	type pendingPingLossDefault struct {
		taskID  uint
		clients []string
	}
	pendingDefaults := make([]pendingPingLossDefault, 0)
	err := db.Transaction(func(tx *gorm.DB) error {
		hasLegacyPingRecords := tx.Migrator().HasTable("ping_records")
		for _, task := range tasks {
			if task == nil || task.Id == 0 {
				return fmt.Errorf("ping task ID is required")
			}
			var existing models.PingTask
			if err := tx.Select("id", "clients").Where("id = ?", task.Id).First(&existing).Error; err != nil {
				return err
			}
			task.Clients = normalizePingClients(task.Clients)
			// 使用 map 显式更新，避免 GORM struct Updates 跳过 false/0/空切片等零值。
			if err := tx.Model(&models.PingTask{}).Where("id = ?", task.Id).Updates(map[string]interface{}{
				"name":        task.Name,
				"clients":     task.Clients,
				"all_clients": task.DefaultOn,
				"type":        task.Type,
				"target":      task.Target,
				"interval":    task.Interval,
			}).Error; err != nil {
				return err
			}
			removedClients := removedPingTaskClients(existing.Clients, task.Clients)
			addedClients := addedPingTaskClients(existing.Clients, task.Clients)
			if len(addedClients) > 0 {
				pendingDefaults = append(pendingDefaults, pendingPingLossDefault{taskID: task.Id, clients: addedClients})
			}
			if len(removedClients) > 0 {
				if err := tx.Where("task_id = ? AND client IN ?", task.Id, removedClients).Delete(&models.PingLossNotification{}).Error; err != nil {
					return err
				}
				if hasLegacyPingRecords {
					if err := tx.Exec("DELETE FROM ping_records WHERE task_id = ? AND client IN ?", task.Id, removedClients).Error; err != nil {
						return fmt.Errorf("delete legacy ping records for task %d: %w", task.Id, err)
					}
				}
				for _, client := range removedClients {
					assignment := metricstore.PingAssignment{Client: client, TaskID: task.Id}
					if err := metricstore.EnqueuePingAssignmentCleanup(tx, assignment); err != nil {
						return fmt.Errorf("queue ping assignment metric cleanup: %w", err)
					}
					removedAssignments = append(removedAssignments, assignment)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(pendingDefaults) > 0 {
		pingLossDefault, defaultErr := notificationdefaults.GetPingLossNotificationDefaultConfig()
		if defaultErr != nil {
			return nil, defaultErr
		}
		for _, pending := range pendingDefaults {
			if err := notificationdefaults.ApplyLoadedPingLossDefaultsToTaskClients(db, pingLossDefault, pending.taskID, pending.clients); err != nil {
				return nil, err
			}
		}
	}
	// Keep just-removed client/task series closed across cleanup retries. The
	// broader task guard held by EditPingTask is released when that edit
	// returns, but a failed metric-store deletion must not let new points enter
	// the same series and then be erased by a later retry.
	metricstore.BlockPingAssignmentWrites(removedAssignments)
	return removedAssignments, nil
}

func editedPingTaskIDs(tasks []*models.PingTask) []uint {
	seen := make(map[uint]struct{}, len(tasks))
	ids := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		if task == nil || task.Id == 0 {
			continue
		}
		if _, ok := seen[task.Id]; ok {
			continue
		}
		seen[task.Id] = struct{}{}
		ids = append(ids, task.Id)
	}
	return ids
}

func addedPingTaskClients(previous, next models.StringArray) []string {
	existing := make(map[string]struct{}, len(previous))
	for _, client := range previous {
		if client == "" {
			continue
		}
		existing[client] = struct{}{}
	}
	added := make([]string, 0)
	seen := make(map[string]struct{})
	for _, client := range next {
		if client == "" {
			continue
		}
		if _, ok := existing[client]; ok {
			continue
		}
		if _, ok := seen[client]; ok {
			continue
		}
		seen[client] = struct{}{}
		added = append(added, client)
	}
	return added
}

func removedPingTaskClients(previous, next models.StringArray) []string {
	remaining := make(map[string]struct{}, len(next))
	for _, client := range next {
		remaining[client] = struct{}{}
	}
	removed := make([]string, 0)
	for _, client := range previous {
		if _, ok := remaining[client]; !ok {
			removed = append(removed, client)
		}
	}
	return removed
}

// normalizePingClients 保持 clients 字段序列化为 JSON 数组，避免空值变成 null。
func normalizePingClients(clients models.StringArray) models.StringArray {
	if clients == nil {
		return models.StringArray{}
	}
	return clients
}

func GetAllPingTasks() ([]models.PingTask, error) {
	db := dbcore.GetDBInstance()
	var tasks []models.PingTask
	if err := db.Order("weight ASC").Order("id ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetPingTasksByClient 获取指定服务器需要执行的延迟监测任务。
func GetPingTasksByClient(uuid string) []models.PingTask {
	tasks, err := getPingTasksByClient(dbcore.GetDBInstance(), uuid)
	if err != nil {
		return nil
	}
	return tasks
}

func getPingTasksByClient(db *gorm.DB, uuid string) ([]models.PingTask, error) {
	var tasks []models.PingTask
	if err := db.Where("clients LIKE ?", `%"`+uuid+`"%`).Order("weight ASC").Order("id ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func UpdatePingTaskOrder(order map[uint]int) error {
	if len(order) == 0 {
		return nil
	}
	if err := updatePingTaskOrder(dbcore.GetDBInstance(), order); err != nil {
		return err
	}
	return ReloadPingSchedule()
}

func updatePingTaskOrder(db *gorm.DB, order map[uint]int) error {
	if len(order) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(order))
	for id := range order {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	return db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&models.PingTask{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return gorm.ErrRecordNotFound
		}
		for _, id := range ids {
			weight := order[id]
			result := tx.Model(&models.PingTask{}).Where("id = ?", id).Update("weight", weight)
			if result.Error != nil {
				return result.Error
			}
		}
		return nil
	})
}

// ping 记录已完全迁移到 metric store（指标 ping.latency_ms），运行期读写全部走
// metric store，旧 ping_records 表不再参与。

func SavePingRecord(record models.PingRecord) error {
	if !utils.IsPingTaskAssigned(record.TaskId, record.Client) {
		return fmt.Errorf("ping task %d is not assigned to client %s", record.TaskId, record.Client)
	}
	return metricstore.WritePingRecord(context.Background(), record)
}

func DeletePingRecords(id []uint) error {
	return metricstore.DeletePingRecordsByTask(context.Background(), id)
}

func DeleteAllPingRecords() error {
	return metricstore.DeleteAllPingRecords(context.Background())
}

func ReloadPingSchedule() error {
	db := dbcore.GetDBInstance()
	var pingTasks []models.PingTask
	if err := db.Find(&pingTasks).Error; err != nil {
		return err
	}
	return utils.ReloadPingSchedule(pingTasks)
}

// AddDefaultOnClientUUID 在新客户端注册后，把该 UUID 追加到所有 default_on=true 的任务的 clients 中（去重）。
func AddDefaultOnClientUUID(uuid string) error {
	if uuid == "" {
		return nil
	}
	db := dbcore.GetDBInstance()
	var tasks []models.PingTask
	if err := db.Where("all_clients = ?", true).Find(&tasks).Error; err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}
	changed := false
	for _, task := range tasks {
		exists := false
		for _, c := range task.Clients {
			if c == uuid {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		next := append(models.StringArray{}, task.Clients...)
		next = append(next, uuid)
		if err := db.Model(&models.PingTask{}).Where("id = ?", task.Id).Update("clients", next).Error; err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return ReloadPingSchedule()
	}
	return nil
}

func GetPingRecords(uuid string, taskId int, start, end time.Time) ([]models.PingRecord, error) {
	return metricstore.GetPingRecords(context.Background(), uuid, taskId, start, end)
}
