package agent

import (
	"testing"
	"time"

	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func TestGuardRemoteDeliveryRefusesWhenDisabled(t *testing.T) {
	called := false
	if GuardRemoteDelivery(func() bool { return false }, func() { called = true }) {
		t.Fatal("guard allowed dispatch while remote management is off")
	}
	if called {
		t.Fatal("dispatch ran while remote management is off")
	}
}

func TestDrainRemoteDeliveryWaitsForGuard(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ok := GuardRemoteDelivery(func() bool { return true }, func() {
			close(started)
			<-release
			EnqueueV2Event("node-gate", v2.MethodAgentExec, v2.ExecParams{TaskID: "in-flight"})
		})
		if !ok {
			t.Error("in-flight dispatch was refused")
		}
	}()
	<-started

	drainStarted := make(chan struct{})
	drained := make(chan []RemovedV2Event, 1)
	go func() {
		removed := DrainRemoteDelivery(func() []RemovedV2Event {
			close(drainStarted)
			return RemoveAllV2EventsByMethods(v2.MethodAgentExec)
		})
		drained <- removed
	}()

	select {
	case <-drainStarted:
		t.Fatal("drain acquired the gate while dispatch still held it")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-done
	<-drainStarted
	removed := <-drained
	if len(removed) != 1 || ExecTaskID(removed[0].Event) != "in-flight" {
		t.Fatalf("drain = %#v", removed)
	}
}
