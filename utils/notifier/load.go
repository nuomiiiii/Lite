package notifier

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	messageevent "github.com/komari-monitor/komari/database/models/messageEvent"
	"github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/pkg/corn"
	logger "github.com/komari-monitor/komari/utils/log"
	"github.com/komari-monitor/komari/utils/messageSender"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LoadNotificationService 管理定时器和任务
type LoadNotificationService struct {
	mu    sync.Mutex
	tasks map[int][]models.LoadNotification
}

var LoadNotificationManager = &LoadNotificationService{
	tasks: make(map[int][]models.LoadNotification),
}

// Reload 重载时间表
func (m *LoadNotificationService) Reload(loadNotifications []models.LoadNotification) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	corn.RemovePrefix("load-notification:")
	m.tasks = make(map[int][]models.LoadNotification)

	// 按Interval分组任务
	taskGroups := make(map[int][]models.LoadNotification)
	for _, task := range loadNotifications {
		taskGroups[task.Interval] = append(taskGroups[task.Interval], task)
	}

	// 为每个唯一的Interval创建定时器
	for interval, tasks := range taskGroups {
		interval := interval
		tasks := append([]models.LoadNotification(nil), tasks...)
		m.tasks[interval] = tasks
		if err := corn.AddFunc(fmt.Sprintf("load-notification:%d", interval), corn.Every(time.Duration(interval)*time.Minute), func() {
			for _, task := range tasks {
				go executeLoadNotificationTask(task)
			}
		}); err != nil {
			return err
		}
	}

	return nil
}

// executeLoadNotificationTask 执行单个LoadNotificationTask
func executeLoadNotificationTask(task models.LoadNotification) {
	if err := evaluateLoadNotificationTask(task, time.Now().UTC(), true); err != nil {
		logger.Errorf("notifier", "Failed to evaluate load notification %d: %v", task.Id, err)
	}
}

func evaluateLoadNotificationTask(task models.LoadNotification, now time.Time, sendNotification bool) error {
	windowStart := now.Add(-time.Duration(task.Interval) * time.Minute)
	evaluations := make([]loadClientEvaluation, 0, len(task.Clients))
	for _, clientUUID := range task.Clients {
		records, err := getRecordsForClient(clientUUID, windowStart, now, task.Metric)
		if err != nil {
			continue
		}
		var client *models.Client
		if metricNeedsClientCapacity(task.Metric) {
			loaded, err := clients.GetClientByUUID(clientUUID)
			if err != nil {
				logger.Errorf("notifier", "Failed to get client info for %s: %v", clientUUID, err)
				continue
			}
			client = &loaded
		}
		active, latestValue, matchedSamples := evaluateMetricThreshold(records, task, client)
		evaluations = append(evaluations, loadClientEvaluation{
			client: clientUUID, active: active, latestValue: latestValue,
			matchedSamples: matchedSamples, totalSamples: len(records),
		})
	}
	notifyClients, current, err := persistLoadClientEvaluations(task, evaluations, now)
	if err != nil {
		return err
	}
	if !current {
		return nil
	}
	if sendNotification && !shouldSkipNotification(task) && len(notifyClients) > 0 {
		sendLoadNotification(notifyClients, task)
		updateLastNotified(task.Id, now)
	}
	return nil
}

type loadClientEvaluation struct {
	client         string
	active         bool
	latestValue    float64
	matchedSamples int
	totalSamples   int
}

func persistLoadClientEvaluations(task models.LoadNotification, evaluations []loadClientEvaluation, now time.Time) ([]string, bool, error) {
	return persistLoadClientEvaluationsWithDB(dbcore.GetDBInstance(), task, evaluations, now)
}

func persistLoadClientEvaluationsWithDB(db *gorm.DB, task models.LoadNotification, evaluations []loadClientEvaluation, now time.Time) ([]string, bool, error) {
	notifyClients := make([]string, 0, len(evaluations))
	current := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var stored models.LoadNotification
		if err := tx.Where("id = ?", task.Id).First(&stored).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		if stored.Name != task.Name || stored.DefaultOn != task.DefaultOn ||
			!reflect.DeepEqual(stored.Clients, task.Clients) ||
			stored.Metric != task.Metric || stored.Threshold != task.Threshold ||
			stored.Ratio != task.Ratio || stored.Interval != task.Interval {
			return nil
		}
		current = true
		assigned := make(map[string]struct{}, len(stored.Clients))
		for _, client := range stored.Clients {
			assigned[client] = struct{}{}
		}
		if err := deleteUnassignedEvaluationStates(tx, task.Id, stored.Clients); err != nil {
			return err
		}
		var existing []models.LoadNotificationState
		if err := tx.Where("notification_id = ?", task.Id).Find(&existing).Error; err != nil {
			return err
		}
		byClient := make(map[string]models.LoadNotificationState, len(existing))
		for _, state := range existing {
			byClient[state.Client] = state
		}
		for _, evaluation := range evaluations {
			if _, ok := assigned[evaluation.client]; !ok {
				continue
			}
			previous := byClient[evaluation.client]
			state := models.LoadNotificationState{
				NotificationID: task.Id, Client: evaluation.client,
				AlertActive: evaluation.active, LastEvaluatedAt: now.UTC(),
				LatestValue: evaluation.latestValue, MatchedSamples: evaluation.matchedSamples,
				TotalSamples: evaluation.totalSamples, SilencedUntil: previous.SilencedUntil,
				SilencedForever: previous.SilencedForever,
			}
			if evaluation.active {
				if previous.AlertActive && previous.ActiveSince != nil {
					state.ActiveSince = previous.ActiveSince
				} else {
					started := now.UTC()
					state.ActiveSince = &started
				}
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "notification_id"}, {Name: "client"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"alert_active", "active_since", "last_evaluated_at", "latest_value",
					"matched_samples", "total_samples", "updated_at",
				}),
			}).Create(&state).Error; err != nil {
				return err
			}
			if evaluation.active && !loadAlertStateSilenced(state, now) {
				notifyClients = append(notifyClients, evaluation.client)
			}
		}
		return nil
	})
	return notifyClients, current, err
}

func deleteUnassignedEvaluationStates(db *gorm.DB, taskID uint, clients models.StringArray) error {
	query := db.Where("notification_id = ?", taskID)
	if len(clients) > 0 {
		query = query.Where("client NOT IN ?", []string(clients))
	}
	return query.Delete(&models.LoadNotificationState{}).Error
}

func loadAlertStateSilenced(state models.LoadNotificationState, now time.Time) bool {
	return state.SilencedForever || (state.SilencedUntil != nil && state.SilencedUntil.After(now))
}

// shouldSkipNotification 检查是否应该跳过通知（冷却期检查）
func shouldSkipNotification(task models.LoadNotification) bool {
	if task.LastNotified == nil || task.LastNotified.IsZero() {
		return false
	}

	// 计算冷却期（使用 interval 作为冷却期）
	cooldownPeriod := time.Duration(task.Interval) * time.Minute
	timeSinceLastNotified := time.Since(*task.LastNotified)

	return timeSinceLastNotified < cooldownPeriod
}

// getRecordsForClient 获取指定客户端在时间窗口内的记录
func getRecordsForClient(clientUUID string, start, end time.Time, metric string) ([]models.Record, error) {
	return records.GetRecordsByClientAndTimeForLoadType(clientUUID, start, end, metric)
}

// checkMetricThreshold 检查指标是否达到阈值
func checkMetricThreshold(records []models.Record, task models.LoadNotification, client *models.Client) bool {
	active, _, _ := evaluateMetricThreshold(records, task, client)
	return active
}

func evaluateMetricThreshold(records []models.Record, task models.LoadNotification, client *models.Client) (bool, float64, int) {
	if len(records) == 0 {
		return false, 0, 0
	}

	// 计算需要达标的最小记录数
	minRequiredRecords := int(float32(len(records)) * task.Ratio)
	if minRequiredRecords == 0 {
		minRequiredRecords = 1
	}

	exceededCount := 0

	for _, record := range records {
		metricValue := getMetricValue(record, task.Metric, client)
		if metricValue >= task.Threshold {
			exceededCount++
		}
	}
	latestValue := float64(getMetricValue(records[len(records)-1], task.Metric, client))
	return exceededCount >= minRequiredRecords, latestValue, exceededCount
}

// getMetricValue 根据指标名称获取记录中的对应值
func getMetricValue(record models.Record, metric string, client *models.Client) float32 {
	switch metric {
	case "cpu":
		return record.Cpu
	case "gpu":
		return record.Gpu
	case "net_in", "netin":
		return bytesPerSecondToMbps(record.NetIn)
	case "net_out", "netout":
		return bytesPerSecondToMbps(record.NetOut)
	case "ram":
		if client != nil && client.MemTotal > 0 {
			return float32(record.Ram) / float32(client.MemTotal) * 100
		}
		return 0
	case "swap":
		if client != nil && client.SwapTotal > 0 {
			return float32(record.Swap) / float32(client.SwapTotal) * 100
		}
		return 0
	case "load":
		return record.Load
	case "temp":
		return record.Temp
	case "disk":
		if client != nil && client.DiskTotal > 0 {
			return float32(record.Disk) / float32(client.DiskTotal) * 100
		}
		return 0
	default:
		// 尝试通过反射获取字段值
		v := reflect.ValueOf(record)
		field := v.FieldByName(metric)
		if field.IsValid() && field.CanInterface() {
			switch field.Kind() {
			case reflect.Float32:
				return float32(field.Float())
			case reflect.Float64:
				return float32(field.Float())
			case reflect.Int, reflect.Int32, reflect.Int64:
				return float32(field.Int())
			}
		}
		return 0
	}
}

func metricNeedsClientCapacity(metric string) bool {
	switch metric {
	case "ram", "swap", "disk":
		return true
	default:
		return false
	}
}

func bytesPerSecondToMbps(bytesPerSecond int64) float32 {
	if bytesPerSecond <= 0 {
		return 0
	}

	// 采用十进制 Mbps：1 Mbps = 1,000,000 bit/s
	return float32(float64(bytesPerSecond) * 8 / 1_000_000)
}

// sendLoadNotification 发送负载通知
func sendLoadNotification(clientUUIDs []string, task models.LoadNotification) {
	ex_clients := []models.Client{}
	for _, clientUUID := range clientUUIDs {
		cl, err := clients.GetClientByUUID(clientUUID)
		if err == nil {
			ex_clients = append(ex_clients, cl)
		}
	}
	if len(ex_clients) == 0 {
		return
	}
	go func() {
		messageSender.SendEvent(models.EventMessage{
			Event:   messageevent.Alert,
			Clients: ex_clients,
			Time:    time.Now().UTC(),
			Emoji:   "⚠️",
			Message: task.Name,
		})
	}()
}

// updateLastNotified 更新最后通知时间
func updateLastNotified(taskId uint, notifyTime time.Time) {
	db := dbcore.GetDBInstance()
	if err := db.Model(&models.LoadNotification{}).Where("id = ?", taskId).Update("last_notified", notifyTime.UTC()).Error; err != nil {
		logger.Errorf("notifier", "Failed to update last_notified for task %d: %v", taskId, err)
	}
}

// ReloadLoadNotificationSchedule 加载或重载时间表
func ReloadLoadNotificationSchedule(loadNotifications []models.LoadNotification) error {
	if err := LoadNotificationManager.Reload(loadNotifications); err != nil {
		return err
	}
	tasks := append([]models.LoadNotification(nil), loadNotifications...)
	go func() {
		now := time.Now().UTC()
		for _, task := range tasks {
			if err := evaluateLoadNotificationTask(task, now, false); err != nil {
				logger.Errorf("notifier", "Failed to reconcile load notification %d: %v", task.Id, err)
			}
		}
	}()
	return nil
}
