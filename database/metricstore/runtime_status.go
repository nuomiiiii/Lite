package metricstore

import (
	"sync"
	"time"

	"github.com/komari-monitor/komari/pkg/metric"
)

// CompactStepInterval is the cadence used by the scheduled incremental
// compactor. It is also used to estimate the next end-of-cycle checkpoint.
const CompactStepInterval = 10 * time.Second

// RuntimeStatus is an in-memory snapshot of metric compaction and SQLite WAL
// checkpoint activity. Reading it never queries or writes the metric database.
type RuntimeStatus struct {
	Driver                        metric.Driver
	Compacting                    bool
	CurrentMetric                 string
	Progress                      int
	Total                         int
	CycleStartedAt                time.Time
	LastStepAt                    time.Time
	LastCycleCompletedAt          time.Time
	LastCheckpointAttemptAt       time.Time
	LastCheckpointSuccessAt       time.Time
	NextCheckpointAt              time.Time
	CheckpointPending             bool
	ConsecutiveCheckpointFailures int
	ConsecutiveCycleFailures      int
	LastError                     string
	CycleWritten                  int

	cycleError string
}

var (
	runtimeStatusMu sync.RWMutex
	runtimeStatus   RuntimeStatus
)

// GetRuntimeStatus returns a race-free copy of the current in-memory state.
func GetRuntimeStatus() RuntimeStatus {
	runtimeStatusMu.RLock()
	defer runtimeStatusMu.RUnlock()
	return runtimeStatus
}

func resetRuntimeStatus(driver metric.Driver) {
	runtimeStatusMu.Lock()
	runtimeStatus = RuntimeStatus{Driver: driver}
	runtimeStatusMu.Unlock()
}

func beginCompactStep(driver metric.Driver, metricName string, index, total int, at time.Time) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()

	if index == 0 || runtimeStatus.Total != total || runtimeStatus.CycleStartedAt.IsZero() {
		runtimeStatus.CycleStartedAt = at
		runtimeStatus.CycleWritten = 0
		runtimeStatus.cycleError = ""
	}
	runtimeStatus.Driver = driver
	runtimeStatus.Compacting = true
	runtimeStatus.CurrentMetric = metricName
	runtimeStatus.Progress = index + 1
	runtimeStatus.Total = total
	runtimeStatus.LastStepAt = at
	remaining := total - index - 1
	if remaining < 0 {
		remaining = 0
	}
	runtimeStatus.NextCheckpointAt = at.Add(time.Duration(remaining) * CompactStepInterval)
}

func finishCompactStep(written int, cycleCompleted bool, err error, at time.Time) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()

	runtimeStatus.Compacting = false
	runtimeStatus.LastStepAt = at
	runtimeStatus.CycleWritten += written
	if err != nil {
		runtimeStatus.cycleError = err.Error()
	}
	if !cycleCompleted {
		return
	}

	runtimeStatus.LastCycleCompletedAt = at
	if runtimeStatus.CheckpointPending {
		runtimeStatus.NextCheckpointAt = at.Add(CompactStepInterval)
	} else {
		runtimeStatus.NextCheckpointAt = at.Add(time.Duration(runtimeStatus.Total) * CompactStepInterval)
	}
	if runtimeStatus.cycleError != "" {
		runtimeStatus.ConsecutiveCycleFailures++
		runtimeStatus.LastError = runtimeStatus.cycleError
	} else {
		runtimeStatus.ConsecutiveCycleFailures = 0
		if !runtimeStatus.CheckpointPending {
			runtimeStatus.LastError = ""
		}
	}
	runtimeStatus.cycleError = ""
}

func finishEmptyCompactCycle(driver metric.Driver, err error, at time.Time) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()

	runtimeStatus.Driver = driver
	runtimeStatus.Compacting = false
	runtimeStatus.CurrentMetric = ""
	runtimeStatus.Progress = 0
	runtimeStatus.Total = 0
	runtimeStatus.CycleStartedAt = at
	runtimeStatus.LastStepAt = at
	runtimeStatus.LastCycleCompletedAt = at
	runtimeStatus.NextCheckpointAt = at.Add(CompactStepInterval)
	if err != nil {
		runtimeStatus.ConsecutiveCycleFailures++
		runtimeStatus.LastError = err.Error()
	} else {
		runtimeStatus.ConsecutiveCycleFailures = 0
		if !runtimeStatus.CheckpointPending {
			runtimeStatus.LastError = ""
		}
	}
}

func checkpointIsPending() bool {
	runtimeStatusMu.RLock()
	defer runtimeStatusMu.RUnlock()
	return runtimeStatus.CheckpointPending
}

func clearCheckpointForExternal(driver metric.Driver) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()
	runtimeStatus.Driver = driver
	runtimeStatus.CheckpointPending = false
	runtimeStatus.ConsecutiveCheckpointFailures = 0
}

func recordCheckpointResult(driver metric.Driver, err error, at time.Time) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()

	runtimeStatus.Driver = driver
	runtimeStatus.LastCheckpointAttemptAt = at
	if err != nil {
		runtimeStatus.CheckpointPending = true
		runtimeStatus.ConsecutiveCheckpointFailures++
		runtimeStatus.LastError = err.Error()
		runtimeStatus.NextCheckpointAt = at.Add(CompactStepInterval)
		return
	}
	runtimeStatus.CheckpointPending = false
	runtimeStatus.ConsecutiveCheckpointFailures = 0
	runtimeStatus.LastCheckpointSuccessAt = at
	if runtimeStatus.ConsecutiveCycleFailures == 0 {
		runtimeStatus.LastError = ""
	}
}
