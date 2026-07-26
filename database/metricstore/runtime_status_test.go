package metricstore

import (
	"errors"
	"testing"
	"time"

	"github.com/komari-monitor/komari/pkg/metric"
)

func TestRuntimeStatusTracksCheckpointFailuresAndRecovery(t *testing.T) {
	previous := GetRuntimeStatus()
	resetRuntimeStatus(metric.DriverSQLite)
	t.Cleanup(func() {
		runtimeStatusMu.Lock()
		runtimeStatus = previous
		runtimeStatusMu.Unlock()
	})

	started := time.Now().UTC()
	checkpointErr := errors.New("database is busy")
	for attempt := 1; attempt <= 3; attempt++ {
		at := started.Add(time.Duration(attempt) * time.Second)
		recordCheckpointResult(metric.DriverSQLite, checkpointErr, at)
		status := GetRuntimeStatus()
		if !status.CheckpointPending || status.ConsecutiveCheckpointFailures != attempt {
			t.Fatalf("checkpoint attempt %d status = %#v", attempt, status)
		}
		if status.LastError != checkpointErr.Error() {
			t.Fatalf("checkpoint attempt %d error = %q", attempt, status.LastError)
		}
		if !status.NextCheckpointAt.Equal(at.Add(CompactStepInterval)) {
			t.Fatalf("checkpoint attempt %d next retry = %s", attempt, status.NextCheckpointAt)
		}
	}

	recoveredAt := started.Add(4 * time.Second)
	recordCheckpointResult(metric.DriverSQLite, nil, recoveredAt)
	status := GetRuntimeStatus()
	if status.CheckpointPending || status.ConsecutiveCheckpointFailures != 0 {
		t.Fatalf("checkpoint failure state was not cleared after recovery: %#v", status)
	}
	if !status.LastCheckpointSuccessAt.Equal(recoveredAt) || status.LastError != "" {
		t.Fatalf("checkpoint recovery was not recorded: %#v", status)
	}
}

func TestRuntimeStatusKeepsPendingRetryEstimateWithoutCountingCycleFailure(t *testing.T) {
	previous := GetRuntimeStatus()
	resetRuntimeStatus(metric.DriverSQLite)
	t.Cleanup(func() {
		runtimeStatusMu.Lock()
		runtimeStatus = previous
		runtimeStatusMu.Unlock()
	})

	started := time.Now().UTC()
	beginCompactStep(metric.DriverSQLite, "cpu", 20, 21, started)
	recordCheckpointResult(metric.DriverSQLite, errors.New("checkpoint timeout"), started.Add(time.Second))
	finished := started.Add(2 * time.Second)
	finishCompactStep(12, true, nil, finished)

	status := GetRuntimeStatus()
	if !status.CheckpointPending || !status.NextCheckpointAt.Equal(finished.Add(CompactStepInterval)) {
		t.Fatalf("pending cycle should retry on the next step: %#v", status)
	}
	if status.ConsecutiveCycleFailures != 0 || status.CycleWritten != 12 {
		t.Fatalf("checkpoint failure should not count as a compaction cycle failure: %#v", status)
	}
}
