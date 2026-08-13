package jsonrpc

import (
	"strconv"
	"strings"

	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/database/models"
)

func pingTasksByStringID(taskList []models.PingTask) map[string]models.PingTask {
	taskMap := make(map[string]models.PingTask, len(taskList))
	for _, task := range taskList {
		taskMap[strconv.FormatUint(uint64(task.Id), 10)] = task
	}
	return taskMap
}

func filterPingRecordsByCurrentAssignments(records []models.PingRecord, taskMap map[string]models.PingTask) []models.PingRecord {
	filtered := make([]models.PingRecord, 0, len(records))
	for _, record := range records {
		task, ok := taskMap[strconv.FormatUint(uint64(record.TaskId), 10)]
		if !ok || !task.AppliesToClient(record.Client) {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

func isPingMetricKey(metricKey string) bool {
	return metricKey == metricstore.MetricPingLatency || metricKey == metricstore.MetricPingLoss
}

func pingMetricSeriesMatchesCurrentAssignment(series publicMetricSeries, taskMap map[string]models.PingTask) bool {
	if !isPingMetricKey(series.MetricKey) {
		return true
	}
	taskID := strings.TrimSpace(series.Tags["task_id"])
	task, ok := taskMap[taskID]
	return ok && task.AppliesToClient(series.EntityID)
}
