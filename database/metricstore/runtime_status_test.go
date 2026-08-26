package metricstore

import (
	"errors"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/pkg/metric"
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
		if !status.NextCheckpointAt.Equal(at.Add(checkpointQuickRetryInterval)) {
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
	if !runtimeStatus.checkpointQuickRetryAt.IsZero() || !runtimeStatus.checkpointFullRetryAt.IsZero() {
		t.Fatalf("checkpoint retry deadlines were not cleared after recovery: %#v", runtimeStatus)
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
	wantRetryAt := started.Add(time.Second).Add(checkpointQuickRetryInterval)
	if !status.CheckpointPending || !status.NextCheckpointAt.Equal(wantRetryAt) {
		t.Fatalf("pending cycle should keep the bounded retry schedule: %#v", status)
	}
	if status.ConsecutiveCycleFailures != 0 || status.CycleWritten != 12 {
		t.Fatalf("checkpoint failure should not count as a compaction cycle failure: %#v", status)
	}
}

func TestRuntimeStatusRebasesCheckpointEstimateAfterSlowPartialStep(t *testing.T) {
	previous := GetRuntimeStatus()
	resetRuntimeStatus(metric.DriverSQLite)
	t.Cleanup(func() {
		runtimeStatusMu.Lock()
		runtimeStatus = previous
		runtimeStatusMu.Unlock()
	})

	started := time.Date(2026, 7, 30, 9, 40, 45, 0, time.UTC)
	beginCompactStep(metric.DriverSQLite, "traffic.up", 19, 21, started)
	status := GetRuntimeStatus()
	if want := started.Add(2 * CompactStepInterval); !status.NextCheckpointAt.Equal(want) {
		t.Fatalf("running estimate = %s, want %s", status.NextCheckpointAt, want)
	}

	finished := started.Add(13 * time.Second)
	finishCompactStep(24, false, nil, finished)
	status = GetRuntimeStatus()
	if want := finished.Add(CompactStepInterval); !status.NextCheckpointAt.Equal(want) {
		t.Fatalf("rebased estimate = %s, want %s", status.NextCheckpointAt, want)
	}
	if status.Progress != 20 || status.Total != 21 || status.Compacting {
		t.Fatalf("partial step status = %#v", status)
	}
}

func TestCheckpointRetryStateSeparatesQuickAndFullRetries(t *testing.T) {
	previous := GetRuntimeStatus()
	resetRuntimeStatus(metric.DriverSQLite)
	t.Cleanup(func() {
		runtimeStatusMu.Lock()
		runtimeStatus = previous
		runtimeStatusMu.Unlock()
	})

	failedAt := time.Now().UTC()
	recordCheckpointResult(metric.DriverSQLite, errors.New("checkpoint timeout"), failedAt)
	if pending, quickDue, fullDue := checkpointRetryState(failedAt.Add(10 * time.Second)); !pending || quickDue || fullDue {
		t.Fatalf("retry state before deadlines = pending %v, quick %v, full %v", pending, quickDue, fullDue)
	}
	if pending, quickDue, fullDue := checkpointRetryState(failedAt.Add(checkpointQuickRetryInterval)); !pending || !quickDue || fullDue {
		t.Fatalf("retry state at quick deadline = pending %v, quick %v, full %v", pending, quickDue, fullDue)
	}
	if pending, quickDue, fullDue := checkpointRetryState(failedAt.Add(checkpointFullRetryInterval)); !pending || !quickDue || !fullDue {
		t.Fatalf("retry state at full deadline = pending %v, quick %v, full %v", pending, quickDue, fullDue)
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
