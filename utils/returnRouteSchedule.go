package utils

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/pkg/corn"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	agentRuntime "github.com/komari-monitor/komari/web/agent"
)

type returnRouteTaskManager struct {
	mu    sync.RWMutex
	tasks map[uint]models.ReturnRouteTask
}

var returnRouteManager = &returnRouteTaskManager{tasks: map[uint]models.ReturnRouteTask{}}

func (m *returnRouteTaskManager) Reload(tasks []models.ReturnRouteTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	corn.RemovePrefix("return-route:")
	m.tasks = make(map[uint]models.ReturnRouteTask, len(tasks))
	for _, task := range tasks {
		if task.Interval <= 0 || task.Client == "" {
			continue
		}
		task := task
		m.tasks[task.Id] = task
		name := fmt.Sprintf("return-route:%d", task.Id)
		if err := corn.AddContextFunc(name, corn.Every(time.Duration(task.Interval)*time.Second), false, func(ctx context.Context) {
			select {
			case <-ctx.Done():
				return
			default:
				DispatchReturnRouteTask(task)
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func DispatchReturnRouteTask(task models.ReturnRouteTask) bool {
	if !agentRuntime.IsV2Client(task.Client) {
		return false
	}
	return agentRuntime.DispatchV2Event(task.Client, v2.MethodAgentRoute, v2.RouteParams{
		TaskID: task.Id, Protocol: task.Protocol, Target: task.Target,
		IPVersion: task.IPVersion, MaxHops: 30,
	})
}

func ReloadReturnRouteSchedule(tasks []models.ReturnRouteTask) error {
	return returnRouteManager.Reload(tasks)
}
