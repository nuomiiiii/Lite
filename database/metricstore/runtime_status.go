package metricstore

import (
	"sync"
	"time"

	"github.com/komari-monitor/komari/pkg/metric"
)

// CompactStepInterval is the cadence used by the scheduled incremental
// compactor. It is also used to estimate the next end-of-cycle checkpoint.
const CompactStepInterval = 10 * time.Second

const (
	checkpointQuickRetryInterval = 30 * time.Second
	checkpointFullRetryInterval  = 5 * time.Minute
)

// DigestHandoffStatus describes a safe-to-retry percentile digest handoff.
type DigestHandoffStatus struct {
	Metric string
	Reason string
	At     time.Time
}

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
	DigestHandoffDeferred         []DigestHandoffStatus
	LastDigestHandoffDeferredAt   time.Time

	cycleError             string
	checkpointQuickRetryAt time.Time
	checkpointFullRetryAt  time.Time
}

var (
	runtimeStatusMu sync.RWMutex
	runtimeStatus   RuntimeStatus
)

// GetRuntimeStatus returns a race-free copy of the current in-memory state.
func GetRuntimeStatus() RuntimeStatus {
	runtimeStatusMu.RLock()
	defer runtimeStatusMu.RUnlock()
	result := runtimeStatus
	result.DigestHandoffDeferred = append([]DigestHandoffStatus(nil), runtimeStatus.DigestHandoffDeferred...)
	return result
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
	// Include the step that just started. A checkpoint cannot finish before the
	// current metric has completed, even when the following scheduler tick is
	// exactly CompactStepInterval away.
	remaining := total - index
	if remaining < 0 {
		remaining = 0
	}
	runtimeStatus.NextCheckpointAt = at.Add(time.Duration(remaining) * CompactStepInterval)
	if runtimeStatus.CheckpointPending && !runtimeStatus.checkpointQuickRetryAt.IsZero() && runtimeStatus.checkpointQuickRetryAt.Before(runtimeStatus.NextCheckpointAt) {
		runtimeStatus.NextCheckpointAt = runtimeStatus.checkpointQuickRetryAt
	}
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
		// Scheduled runs that overlap a slow metric are intentionally skipped.
		// Rebase the estimate on the actual finish time so the UI never keeps the
		// optimistic timestamp calculated before that slow step ran.
		remaining := runtimeStatus.Total - runtimeStatus.Progress
		if remaining < 0 {
			remaining = 0
		}
		runtimeStatus.NextCheckpointAt = at.Add(time.Duration(remaining) * CompactStepInterval)
		if runtimeStatus.CheckpointPending && !runtimeStatus.checkpointQuickRetryAt.IsZero() && runtimeStatus.checkpointQuickRetryAt.Before(runtimeStatus.NextCheckpointAt) {
			runtimeStatus.NextCheckpointAt = runtimeStatus.checkpointQuickRetryAt
		}
		return
	}

	runtimeStatus.LastCycleCompletedAt = at
	if runtimeStatus.CheckpointPending {
		runtimeStatus.NextCheckpointAt = runtimeStatus.checkpointQuickRetryAt
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
	if runtimeStatus.CheckpointPending {
		runtimeStatus.NextCheckpointAt = runtimeStatus.checkpointQuickRetryAt
	} else {
		runtimeStatus.NextCheckpointAt = at.Add(CompactStepInterval)
	}
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

func recordDigestHandoffDeferred(metricName, reason string, at time.Time) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()

	for index := range runtimeStatus.DigestHandoffDeferred {
		if runtimeStatus.DigestHandoffDeferred[index].Metric == metricName {
			runtimeStatus.DigestHandoffDeferred[index].Reason = reason
			runtimeStatus.DigestHandoffDeferred[index].At = at
			runtimeStatus.LastDigestHandoffDeferredAt = at
			return
		}
	}
	runtimeStatus.DigestHandoffDeferred = append(runtimeStatus.DigestHandoffDeferred, DigestHandoffStatus{
		Metric: metricName,
		Reason: reason,
		At:     at,
	})
	runtimeStatus.LastDigestHandoffDeferredAt = at
}

func clearDigestHandoffDeferred(metricName string) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()

	for index := range runtimeStatus.DigestHandoffDeferred {
		if runtimeStatus.DigestHandoffDeferred[index].Metric != metricName {
			continue
		}
		runtimeStatus.DigestHandoffDeferred = append(
			runtimeStatus.DigestHandoffDeferred[:index],
			runtimeStatus.DigestHandoffDeferred[index+1:]...,
		)
		return
	}
}

func checkpointIsPending() bool {
	runtimeStatusMu.RLock()
	defer runtimeStatusMu.RUnlock()
	return runtimeStatus.CheckpointPending
}

func checkpointRetryState(at time.Time) (pending, quickDue, fullDue bool) {
	runtimeStatusMu.RLock()
	defer runtimeStatusMu.RUnlock()
	if !runtimeStatus.CheckpointPending {
		return false, false, false
	}
	quickDue = runtimeStatus.checkpointQuickRetryAt.IsZero() || !at.Before(runtimeStatus.checkpointQuickRetryAt)
	fullDue = runtimeStatus.checkpointFullRetryAt.IsZero() || !at.Before(runtimeStatus.checkpointFullRetryAt)
	return true, quickDue, fullDue
}

func deferFullCheckpointRetry(at time.Time) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()
	if runtimeStatus.CheckpointPending {
		runtimeStatus.checkpointFullRetryAt = at.Add(checkpointFullRetryInterval)
	}
}

func clearCheckpointForExternal(driver metric.Driver) {
	runtimeStatusMu.Lock()
	defer runtimeStatusMu.Unlock()
	runtimeStatus.Driver = driver
	runtimeStatus.CheckpointPending = false
	runtimeStatus.ConsecutiveCheckpointFailures = 0
	runtimeStatus.checkpointQuickRetryAt = time.Time{}
	runtimeStatus.checkpointFullRetryAt = time.Time{}
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
		runtimeStatus.checkpointQuickRetryAt = at.Add(checkpointQuickRetryInterval)
		if runtimeStatus.checkpointFullRetryAt.IsZero() {
			runtimeStatus.checkpointFullRetryAt = at.Add(checkpointFullRetryInterval)
		}
		runtimeStatus.NextCheckpointAt = runtimeStatus.checkpointQuickRetryAt
		return
	}
	runtimeStatus.CheckpointPending = false
	runtimeStatus.ConsecutiveCheckpointFailures = 0
	runtimeStatus.checkpointQuickRetryAt = time.Time{}
	runtimeStatus.checkpointFullRetryAt = time.Time{}
	runtimeStatus.LastCheckpointSuccessAt = at
	if runtimeStatus.ConsecutiveCycleFailures == 0 {
		runtimeStatus.LastError = ""
	}
}
