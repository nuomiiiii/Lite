package metricstore

import (
	"errors"
	"sync"
)

var ErrMetricWriteBlocked = errors.New("metric writes are blocked for a deleted target")

var deletionGuards = struct {
	sync.RWMutex
	entities        map[string]struct{}
	pingTasks       map[uint]struct{}
	pingAssignments map[PingAssignment]struct{}
}{
	entities:        make(map[string]struct{}),
	pingTasks:       make(map[uint]struct{}),
	pingAssignments: make(map[PingAssignment]struct{}),
}

func BlockEntityWrites(entityID string) {
	if entityID == "" {
		return
	}
	deletionGuards.Lock()
	deletionGuards.entities[entityID] = struct{}{}
	deletionGuards.Unlock()
}

func UnblockEntityWrites(entityID string) {
	deletionGuards.Lock()
	delete(deletionGuards.entities, entityID)
	deletionGuards.Unlock()
}

func EntityWritesBlocked(entityID string) bool {
	deletionGuards.RLock()
	_, blocked := deletionGuards.entities[entityID]
	deletionGuards.RUnlock()
	return blocked
}

func BlockPingTaskWrites(taskIDs []uint) {
	deletionGuards.Lock()
	for _, taskID := range taskIDs {
		deletionGuards.pingTasks[taskID] = struct{}{}
	}
	deletionGuards.Unlock()
}

func UnblockPingTaskWrites(taskIDs []uint) {
	deletionGuards.Lock()
	for _, taskID := range taskIDs {
		delete(deletionGuards.pingTasks, taskID)
	}
	deletionGuards.Unlock()
}

func PingTaskWritesBlocked(taskID uint) bool {
	deletionGuards.RLock()
	_, blocked := deletionGuards.pingTasks[taskID]
	deletionGuards.RUnlock()
	return blocked
}

func BlockPingAssignmentWrites(assignments []PingAssignment) {
	deletionGuards.Lock()
	for _, assignment := range assignments {
		if assignment.Client != "" && assignment.TaskID != 0 {
			deletionGuards.pingAssignments[assignment] = struct{}{}
		}
	}
	deletionGuards.Unlock()
}

func UnblockPingAssignmentWrites(assignments []PingAssignment) {
	deletionGuards.Lock()
	for _, assignment := range assignments {
		delete(deletionGuards.pingAssignments, assignment)
	}
	deletionGuards.Unlock()
}

func PingAssignmentWritesBlocked(assignment PingAssignment) bool {
	deletionGuards.RLock()
	_, blocked := deletionGuards.pingAssignments[assignment]
	deletionGuards.RUnlock()
	return blocked
}
