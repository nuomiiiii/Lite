package agent

import (
	"testing"

	"github.com/nuomiiiii/lite/database/metricstore"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
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

func TestConfigEventsCoalesceToLatestRevisionPerNode(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	EnqueueV2Event("node-a", v2.MethodAgentConfig, v2.ConfigParams{Revision: 1})
	latest := EnqueueV2Event("node-a", v2.MethodAgentConfig, v2.ConfigParams{Revision: 2})
	EnqueueV2Event("node-a", v2.MethodAgentExec, v2.ExecParams{TaskID: "keep"})
	EnqueueV2Event("node-b", v2.MethodAgentConfig, v2.ConfigParams{Revision: 7})

	events := TakeV2Events("node-a", nil, 16)
	if len(events) != 2 || events[0].ID != latest.ID || events[0].Method != v2.MethodAgentConfig || events[1].Method != v2.MethodAgentExec {
		t.Fatalf("node-a coalesced events = %#v", events)
	}
	var config v2.ConfigParams
	if err := bindV2EventParams(events[0].Params, &config); err != nil || config.Revision != 2 {
		t.Fatalf("latest config = %+v, %v", config, err)
	}
	other := TakeV2Events("node-b", nil, 16)
	if len(other) != 1 || other[0].Method != v2.MethodAgentConfig {
		t.Fatalf("node-b config changed = %#v", other)
	}
}

func TestOfflineConfigQueueKeepsOnlyNewestRevisionAcrossRepeatedSaves(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	for revision := uint64(1); revision <= 25; revision++ {
		EnqueueV2Event("offline-node", v2.MethodAgentConfig, v2.ConfigParams{Revision: revision})
	}

	events := TakeV2Events("offline-node", nil, v2EventQueueLimit)
	if len(events) != 1 || events[0].Method != v2.MethodAgentConfig {
		t.Fatalf("offline config queue = %#v, want one config event", events)
	}
	var config v2.ConfigParams
	if err := bindV2EventParams(events[0].Params, &config); err != nil || config.Revision != 25 {
		t.Fatalf("queued config = %+v, %v; want revision 25", config, err)
	}
}
