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
func TestRuntimeStatusTracksDigestHandoffDeferredWithoutFailure(t *testing.T) {
	previous := GetRuntimeStatus()
	resetRuntimeStatus(metric.DriverSQLite)
	t.Cleanup(func() {
		runtimeStatusMu.Lock()
		runtimeStatus = previous
		runtimeStatusMu.Unlock()
	})

	at := time.Now().UTC()
	recordDigestHandoffDeferred("cpu.usage", "摘要校验暂未通过", at)
	recordDigestHandoffDeferred("load.average", "细粒度摘要尚未完整", at.Add(time.Second))
	recordDigestHandoffDeferred("cpu.usage", "摘要校验暂未通过（已重试）", at.Add(2*time.Second))

	status := GetRuntimeStatus()
	if len(status.DigestHandoffDeferred) != 2 {
		t.Fatalf("deferred status count = %d, want 2: %#v", len(status.DigestHandoffDeferred), status)
	}
	if status.ConsecutiveCycleFailures != 0 || status.ConsecutiveCheckpointFailures != 0 || status.LastError != "" {
		t.Fatalf("safe deferral changed failure state: %#v", status)
	}
	if !status.LastDigestHandoffDeferredAt.Equal(at.Add(2 * time.Second)) {
		t.Fatalf("latest deferred time was not updated: %#v", status)
	}

	status.DigestHandoffDeferred[0].Reason = "mutated copy"
	if GetRuntimeStatus().DigestHandoffDeferred[0].Reason == "mutated copy" {
		t.Fatal("GetRuntimeStatus returned the internal deferred slice")
	}

	clearDigestHandoffDeferred("cpu.usage")
	status = GetRuntimeStatus()
	if len(status.DigestHandoffDeferred) != 1 || status.DigestHandoffDeferred[0].Metric != "load.average" {
		t.Fatalf("successful retry did not clear only its metric: %#v", status)
	}
}
