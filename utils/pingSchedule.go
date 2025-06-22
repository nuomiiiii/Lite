package utils

import (
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/ws"
)

// PingTaskManager 管理定时器和任务
type PingTaskManager struct {
	mu       sync.Mutex
	tickers  map[int]*time.Ticker
	tasks    map[int][]models.PingTask
	stopChan chan struct{}
}

var manager = &PingTaskManager{
	tickers:  make(map[int]*time.Ticker),
	tasks:    make(map[int][]models.PingTask),
	stopChan: make(chan struct{}),
}

// Reload 重载时间表
func (m *PingTaskManager) Reload(pingTasks []models.PingTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 停止所有现有定时器
	for _, ticker := range m.tickers {
		ticker.Stop()
	}
	m.tickers = make(map[int]*time.Ticker)
	m.tasks = make(map[int][]models.PingTask)

	// 按Interval分组任务
	taskGroups := make(map[int][]models.PingTask)
	for _, task := range pingTasks {
		taskGroups[task.Interval] = append(taskGroups[task.Interval], task)
	}

	// 为每个唯一的Interval创建定时器
	for interval, tasks := range taskGroups {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		m.tickers[interval] = ticker
		m.tasks[interval] = tasks

		go func(ticker *time.Ticker, tasks []models.PingTask) {
			for {
				select {
				case <-ticker.C:
					for _, task := range tasks {
						go executePingTask(task)
					}
				case <-m.stopChan:
					return
				}
			}
		}(ticker, tasks)
	}

	return nil
}

// executePingTask 执行单个PingTask
func executePingTask(task models.PingTask) {
	clients := ws.GetConnectedClients()
	var message struct {
		TaskID  uint   `json:"ping_task_id"`
		Message string `json:"message"`
		Type    string `json:"ping_type"`
		Target  string `json:"ping_target"`
	}
	for _, clientUUID := range task.Clients {
		if conn, exists := clients[clientUUID]; exists {
			if conn == nil {
				continue
			}
			message.Message = "ping"
			message.TaskID = task.Id
			message.Type = task.Type
			message.Target = task.Target
			if err := conn.WriteJSON(message); err != nil {
				continue
			}
		}
	}
}

// ReloadPingSchedule 加载或重载时间表
func ReloadPingSchedule(pingTasks []models.PingTask) error {
	return manager.Reload(pingTasks)
}
