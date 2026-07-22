package metricstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStoreOperationGateAllowsConcurrentSharedOperations(t *testing.T) {
	gate := newStoreOperationGate()
	if err := gate.AcquireShared(context.Background()); err != nil {
		t.Fatalf("acquire first shared lease: %v", err)
	}
	defer gate.ReleaseShared()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := gate.AcquireShared(ctx); err != nil {
		t.Fatalf("acquire second shared lease: %v", err)
	}
	gate.ReleaseShared()
}

func TestStoreOperationGateExclusiveWaitsForSharedOperations(t *testing.T) {
	gate := newStoreOperationGate()
	if err := gate.AcquireShared(context.Background()); err != nil {
		t.Fatalf("acquire shared lease: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		if err := gate.Acquire(context.Background()); err == nil {
			close(acquired)
		}
	}()

	select {
	case <-acquired:
		t.Fatal("exclusive lease acquired while shared work was active")
	case <-time.After(20 * time.Millisecond):
	}

	gate.ReleaseShared()
	select {
	case <-acquired:
		gate.Release()
	case <-time.After(time.Second):
		t.Fatal("exclusive lease did not acquire after shared work completed")
	}
}

func TestStoreOperationGateWaitsRespectContext(t *testing.T) {
	gate := newStoreOperationGate()
	if !gate.TryAcquire() {
		t.Fatal("acquire exclusive lease")
	}
	defer gate.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.AcquireShared(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shared acquire error = %v, want %v", err, context.Canceled)
	}
}
