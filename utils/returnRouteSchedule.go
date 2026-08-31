package utils

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/corn"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agentRuntime "github.com/nuomiiiii/lite/web/agent"
)

type returnRouteTaskManager struct {
	mu    sync.RWMutex
	tasks map[uint]models.ReturnRouteTask
}

var returnRouteManager = &returnRouteTaskManager{tasks: map[uint]models.ReturnRouteTask{}}

const returnRouteProbeTimeout = 10 * time.Minute

var returnRouteProbes = struct {
	sync.Mutex
	started map[uint]time.Time
}{started: map[uint]time.Time{}}

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
				if ReturnRouteProbeInFlight(task.Id) {
					return
				}
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
	dispatched := agentRuntime.DispatchV2Event(task.Client, v2.MethodAgentRoute, v2.RouteParams{
		TaskID: task.Id, Protocol: task.Protocol, Target: task.Target,
		IPVersion: task.IPVersion, MaxHops: 30,
	})
	if dispatched {
		StartReturnRouteProbe(task.Id)
	}
	return dispatched
}

func StartReturnRouteProbe(taskID uint) {
	if taskID == 0 {
		return
	}
	returnRouteProbes.Lock()
	if _, exists := returnRouteProbes.started[taskID]; !exists {
		returnRouteProbes.started[taskID] = time.Now()
	}
	returnRouteProbes.Unlock()
}

func ReturnRouteProbeInFlight(taskID uint) bool {
	if taskID == 0 {
		return false
	}
	returnRouteProbes.Lock()
	defer returnRouteProbes.Unlock()
	started, exists := returnRouteProbes.started[taskID]
	if !exists {
		return false
	}
	if time.Since(started) >= returnRouteProbeTimeout {
		delete(returnRouteProbes.started, taskID)
		return false
	}
	return true
}

func ProbingReturnRouteTaskIDs() []uint {
	returnRouteProbes.Lock()
	defer returnRouteProbes.Unlock()
	now := time.Now()
	ids := make([]uint, 0, len(returnRouteProbes.started))
	for id, started := range returnRouteProbes.started {
		if now.Sub(started) >= returnRouteProbeTimeout {
			delete(returnRouteProbes.started, id)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func FinishReturnRouteProbe(taskID uint) {
	returnRouteProbes.Lock()
	delete(returnRouteProbes.started, taskID)
	returnRouteProbes.Unlock()
}

func ReloadReturnRouteSchedule(tasks []models.ReturnRouteTask) error {
	return returnRouteManager.Reload(tasks)
}

// IsReturnRouteClientOnline reports whether the agent still has a live connection
// to Lite. V2 pull-only presence is not enough for mainland reachability.
func IsReturnRouteClientOnline(uuid string) bool {
	return agentRuntime.GetConnectedClients()[uuid] != nil
}
