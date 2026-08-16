package notifier

import (
	"errors"
	"fmt"
	"math"
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
	mu        sync.Mutex
	tasks     map[int][]models.LoadNotification
	executing map[uint]struct{}
}

var LoadNotificationManager = &LoadNotificationService{
	tasks:     make(map[int][]models.LoadNotification),
	executing: make(map[uint]struct{}),
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
	if !LoadNotificationManager.beginExecution(task.Id) {
		return
	}
	defer LoadNotificationManager.endExecution(task.Id)

	if err := evaluateLoadNotificationTask(task, time.Now().UTC(), true); err != nil {
		logger.Errorf("notifier", "Failed to evaluate load notification %d: %v", task.Id, err)
	}
}

func (m *LoadNotificationService) beginExecution(taskID uint) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.executing == nil {
		m.executing = make(map[uint]struct{})
	}
	if _, running := m.executing[taskID]; running {
		return false
	}
	m.executing[taskID] = struct{}{}
	return true
}

func (m *LoadNotificationService) endExecution(taskID uint) {
	m.mu.Lock()
	delete(m.executing, taskID)
	m.mu.Unlock()
}

func evaluateLoadNotificationTask(task models.LoadNotification, now time.Time, sendNotification bool) error {
	windowStart := now.Add(-time.Duration(task.Interval) * time.Minute)
	evaluations := make([]loadClientEvaluation, 0, len(task.Clients))
	for _, clientUUID := range task.Clients {
		records, err := getRecordsForClient(clientUUID, windowStart, now, task.Metric)
		if err != nil {
			logger.Errorf("notifier", "Failed to read %s records for %s in load notification %d: %v", task.Metric, clientUUID, task.Id, err)
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
	if !sendNotification {
		return nil
	}
	recoveryClients, err := pendingLoadRecoveryClientsWithDB(dbcore.GetDBInstance(), task.Id)
	if err != nil {
		return fmt.Errorf("list pending load recoveries %d: %w", task.Id, err)
	}
	var sendErrors []error
	if len(recoveryClients) > 0 {
		if err := sendLoadRecoveryNotification(recoveryClients, task, now); err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("send load recovery %d: %w", task.Id, err))
		}
	}
	if len(notifyClients) > 0 {
		if err := sendLoadNotification(notifyClients, task, now); err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("send load notification %d: %w", task.Id, err))
		}
	}
	return errors.Join(sendErrors...)
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
			fingerprint := models.LoadNotificationRuleFingerprint(stored)
			if previous.RuleFingerprint != fingerprint {
				previous = models.LoadNotificationState{}
			}
			state := models.LoadNotificationState{
				NotificationID: task.Id, Client: evaluation.client,
				RuleFingerprint: fingerprint,
				AlertActive:     evaluation.active, LastEvaluatedAt: now.UTC(),
				LatestValue: evaluation.latestValue, MatchedSamples: evaluation.matchedSamples,
				TotalSamples: evaluation.totalSamples, LastNotified: previous.LastNotified,
				RecoveryPending: previous.RecoveryPending, SilencedUntil: previous.SilencedUntil,
				SilencedForever: previous.SilencedForever,
			}
			if evaluation.active {
				state.RecoveryPending = false
				if previous.AlertActive && previous.ActiveSince != nil {
					state.ActiveSince = previous.ActiveSince
				} else {
					started := now.UTC()
					state.ActiveSince = &started
				}
			} else if previous.AlertActive && previous.LastNotified != nil && !loadAlertStateSilenced(previous, now) {
				state.RecoveryPending = true
			} else if !previous.RecoveryPending {
				state.LastNotified = nil
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "notification_id"}, {Name: "client"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"rule_fingerprint", "alert_active", "active_since", "last_evaluated_at",
					"latest_value", "matched_samples", "total_samples", "last_notified",
					"recovery_pending", "silenced_until", "silenced_forever", "updated_at",
				}),
			}).Create(&state).Error; err != nil {
				return err
			}
			if evaluation.active && !loadAlertStateSilenced(state, now) && !shouldSkipLoadClientNotification(previous.LastNotified, stored.Interval, now) {
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

func shouldSkipLoadClientNotification(lastNotified *time.Time, interval int, now time.Time) bool {
	if lastNotified == nil || lastNotified.IsZero() {
		return false
	}
	cooldownPeriod := time.Duration(interval) * time.Minute
	timeSinceLastNotified := now.UTC().Sub(lastNotified.UTC())
	return timeSinceLastNotified < cooldownPeriod
}

func pendingLoadRecoveryClientsWithDB(db *gorm.DB, taskID uint) ([]string, error) {
	var clients []string
	err := db.Model(&models.LoadNotificationState{}).
		Where("notification_id = ? AND recovery_pending = ?", taskID, true).
		Order("client ASC").Pluck("client", &clients).Error
	return clients, err
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
	minRequiredRecords := int(math.Ceil(float64(len(records)) * float64(task.Ratio)))
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
func sendLoadNotification(clientUUIDs []string, task models.LoadNotification, now time.Time) error {
	return sendLoadNotificationWith(dbcore.GetDBInstance(), clientUUIDs, task, now, clients.GetClientByUUID, messageSender.SendEvent)
}

func sendLoadNotificationWith(db *gorm.DB, clientUUIDs []string, task models.LoadNotification, now time.Time, lookup func(string) (models.Client, error), send func(models.EventMessage) error) error {
	return sendLoadClientNotificationsWith(db, clientUUIDs, task, now, false, lookup, send)
}

func sendLoadRecoveryNotification(clientUUIDs []string, task models.LoadNotification, now time.Time) error {
	return sendLoadRecoveryNotificationWith(dbcore.GetDBInstance(), clientUUIDs, task, now, clients.GetClientByUUID, messageSender.SendEvent)
}

func sendLoadRecoveryNotificationWith(db *gorm.DB, clientUUIDs []string, task models.LoadNotification, now time.Time, lookup func(string) (models.Client, error), send func(models.EventMessage) error) error {
	return sendLoadClientNotificationsWith(db, clientUUIDs, task, now, true, lookup, send)
}

func sendLoadClientNotificationsWith(db *gorm.DB, clientUUIDs []string, task models.LoadNotification, now time.Time, recovery bool, lookup func(string) (models.Client, error), send func(models.EventMessage) error) error {
	var sendErrors []error
	alertSent := false
	for _, clientUUID := range clientUUIDs {
		client, err := lookup(clientUUID)
		if err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("load client %s: %w", clientUUID, err))
			continue
		}
		emoji, message := "⚠️", task.Name
		if recovery {
			emoji, message = "✅", task.Name+" 已恢复"
		}
		if err := send(models.EventMessage{
			Event: messageevent.Alert, Clients: []models.Client{client}, Time: now.UTC(), Emoji: emoji, Message: message,
		}); err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("send client %s: %w", clientUUID, err))
			continue
		}
		updates := map[string]any{"last_notified": now.UTC()}
		query := db.Model(&models.LoadNotificationState{}).
			Where("notification_id = ? AND client = ?", task.Id, clientUUID)
		if recovery {
			updates["last_notified"] = nil
			updates["recovery_pending"] = false
			query = query.Where("recovery_pending = ?", true)
		} else {
			query = query.Where("alert_active = ?", true)
		}
		result := query.Updates(updates)
		if result.Error != nil {
			sendErrors = append(sendErrors, fmt.Errorf("record client %s delivery: %w", clientUUID, result.Error))
			continue
		}
		if result.RowsAffected == 0 {
			sendErrors = append(sendErrors, fmt.Errorf("record client %s delivery: %w", clientUUID, gorm.ErrRecordNotFound))
			continue
		}
		alertSent = alertSent || !recovery
	}
	if alertSent {
		if err := updateLastNotifiedWithDB(db, task.Id, now); err != nil {
			sendErrors = append(sendErrors, err)
		}
	}
	return errors.Join(sendErrors...)
}

// updateLastNotified 更新最后通知时间
func updateLastNotifiedWithDB(db *gorm.DB, taskID uint, notifyTime time.Time) error {
	result := db.Model(&models.LoadNotification{}).Where("id = ?", taskID).Update("last_notified", notifyTime.UTC())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
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
