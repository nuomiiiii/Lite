package agent

import (
	"testing"

	"github.com/komari-monitor/komari/database/metricstore"
	v2 "github.com/komari-monitor/komari/protocol/v2"
)

func TestRemoveV2EventsByMethodsPreservesUnrelatedEvents(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	EnqueueV2Event("node-a", v2.MethodAgentExec, v2.ExecParams{TaskID: "task"})
	EnqueueV2Event("node-a", v2.MethodAgentRemote, v2.RemoteRequestParams{RequestID: "remote"})
	EnqueueV2Event("node-a", v2.MethodAgentPing, v2.PingParams{TaskID: 7})
	EnqueueV2Event("node-b", v2.MethodAgentExec, v2.ExecParams{TaskID: "other"})

	RemoveV2EventsByMethods("node-a", v2.MethodAgentExec, v2.MethodAgentRemote)
	events := TakeV2Events("node-a", nil, 16)
	if len(events) != 1 || events[0].Method != v2.MethodAgentPing {
		t.Fatalf("node-a events after protection = %#v", events)
	}
	other := TakeV2Events("node-b", nil, 16)
	if len(other) != 1 || other[0].Method != v2.MethodAgentExec {
		t.Fatalf("unrelated node events changed = %#v", other)
	}
}

func TestRemoveV2EventQueueClearsOnlyDeletedClient(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	EnqueueV2Event("node-a", v2.MethodAgentExec, v2.ExecParams{TaskID: "task-a"})
	EnqueueV2Event("node-b", v2.MethodAgentExec, v2.ExecParams{TaskID: "task-b"})
	RemoveV2EventQueue("node-a")
	metricstore.BlockEntityWrites("node-a")
	t.Cleanup(func() { metricstore.UnblockEntityWrites("node-a") })
	if event := EnqueueV2Event("node-a", v2.MethodAgentExec, v2.ExecParams{TaskID: "late"}); event.ID != "" {
		t.Fatalf("blocked node accepted late event: %#v", event)
	}

	if events := TakeV2Events("node-a", nil, 16); len(events) != 0 {
		t.Fatalf("deleted node events = %#v", events)
	}
	if events := TakeV2Events("node-b", nil, 16); len(events) != 1 {
		t.Fatalf("unrelated node events = %#v", events)
	}
}
