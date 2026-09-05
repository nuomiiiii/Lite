package agent

import (
	"testing"
	"time"

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

func TestRemoveAllV2EventsByMethodsClearsEveryQueue(t *testing.T) {
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
	EnqueueV2Event("node-b", v2.MethodAgentRemote, v2.RemoteRequestParams{RequestID: "remote"})
	EnqueueV2Event("node-b", v2.MethodAgentPing, v2.PingParams{TaskID: 3})
	RemoveAllV2EventsByMethods(v2.MethodAgentExec, v2.MethodAgentRemote)
	if events := TakeV2Events("node-a", nil, 16); len(events) != 0 {
		t.Fatalf("node-a remote events remained: %#v", events)
	}
	if events := TakeV2Events("node-b", nil, 16); len(events) != 1 || events[0].Method != v2.MethodAgentPing {
		t.Fatalf("node-b events = %#v", events)
	}
}

func TestRemoteEventsExpireWithin45Seconds(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	event := EnqueueV2Event("node-a", v2.MethodAgentRemote, v2.RemoteRequestParams{RequestID: "session-1"})
	if event.ID == "" {
		t.Fatal("remote event was not queued")
	}
	ttl := event.ExpiresAt.Sub(event.CreatedAt)
	if ttl != pendingRemoteEventTTL {
		t.Fatalf("remote event ttl = %s, want %s", ttl, pendingRemoteEventTTL)
	}
}

func TestRemoveV2RemoteRequestDropsQueuedEvent(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	EnqueueV2Event("node-a", v2.MethodAgentRemote, v2.RemoteRequestParams{RequestID: "session-1"})
	EnqueueV2Event("node-a", v2.MethodAgentRemote, v2.RemoteRequestParams{RequestID: "session-2"})
	RemoveV2RemoteRequest("node-a", "session-1")
	events := TakeV2Events("node-a", nil, 16)
	if len(events) != 1 {
		t.Fatalf("queued remote events = %#v", events)
	}
	var params v2.RemoteRequestParams
	if err := bindV2EventParams(events[0].Params, &params); err != nil || params.RequestID != "session-2" {
		t.Fatalf("remaining remote request = %+v %v", params, err)
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

func TestEnqueueV2EventRefusesWhenQueueIsFullOfExec(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	for i := 0; i < v2EventQueueLimit; i++ {
		event := EnqueueV2Event("node-full", v2.MethodAgentExec, v2.ExecParams{TaskID: "task-" + itoa(i)})
		if event.ID == "" {
			t.Fatalf("exec %d was not queued", i)
		}
	}
	if event := EnqueueV2Event("node-full", v2.MethodAgentExec, v2.ExecParams{TaskID: "overflow"}); event.ID != "" {
		t.Fatal("queue accepted an exec after it was full")
	}
	if event := EnqueueV2Event("node-full", v2.MethodAgentRemote, v2.RemoteRequestParams{RequestID: "late"}); event.ID != "" {
		t.Fatal("queue dropped exec events to enqueue remote")
	}
	events := TakeV2Events("node-full", nil, v2EventQueueLimit+1)
	if len(events) != v2EventQueueLimit {
		t.Fatalf("queued events = %d, want %d", len(events), v2EventQueueLimit)
	}
}

func TestConfigStillCoalescesWhenQueueIsOtherwiseFull(t *testing.T) {
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
	for i := 0; i < v2EventQueueLimit-1; i++ {
		EnqueueV2Event("node-a", v2.MethodAgentExec, v2.ExecParams{TaskID: "task-" + itoa(i)})
	}
	latest := EnqueueV2Event("node-a", v2.MethodAgentConfig, v2.ConfigParams{Revision: 9})
	if latest.ID == "" {
		t.Fatal("coalesced config was refused")
	}
	events := TakeV2Events("node-a", nil, v2EventQueueLimit)
	if len(events) != v2EventQueueLimit {
		t.Fatalf("events = %d, want %d", len(events), v2EventQueueLimit)
	}
}

func TestRemoveAllRemoteEventsAlsoClearsQueuedExec(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	EnqueueV2Event("node-a", v2.MethodAgentExec, v2.ExecParams{TaskID: "keep"})
	EnqueueV2Event("node-a", v2.MethodAgentRemote, v2.RemoteRequestParams{RequestID: "drop"})
	EnqueueV2Event("node-a", v2.MethodAgentPing, v2.PingParams{TaskID: 9})
	removed := RemoveAllV2EventsByMethods(v2.MethodAgentRemote, v2.MethodAgentExec)
	if len(removed) != 2 {
		t.Fatalf("removed = %#v", removed)
	}
	events := TakeV2Events("node-a", nil, 8)
	if len(events) != 1 || events[0].Method != v2.MethodAgentPing {
		t.Fatalf("events after disabling remote = %#v", events)
	}
}

func TestDispatchV2ExecEventEnqueuesWithEventID(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})

	queued, notified := DispatchV2ExecEvent("node-exec", v2.ExecParams{TaskID: "task-1", Command: "hostname"})
	if !queued {
		t.Fatal("exec event was not queued")
	}
	if notified {
		t.Fatal("offline node was treated as websocket-notified")
	}
	events := TakeV2Events("node-exec", nil, 8)
	if len(events) != 1 || events[0].ID == "" || events[0].Method != v2.MethodAgentExec {
		t.Fatalf("queued exec = %#v", events)
	}
}

func TestSweepExpiredExecEventsPersistDeliveryTimeout(t *testing.T) {
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
		SetExpiredV2ExecHandler(nil)
	})

	var gotUUID, gotTask string
	SetExpiredV2ExecHandler(func(uuid, taskID string) {
		gotUUID = uuid
		gotTask = taskID
	})
	event := EnqueueV2Event("node-timeout", v2.MethodAgentExec, v2.ExecParams{TaskID: "task-late", Command: "true"})
	if event.ID == "" {
		t.Fatal("exec event was not queued")
	}
	v2EventMu.Lock()
	q := v2EventQueues["node-timeout"]
	q.events[0].ExpiresAt = time.Now().UTC().Add(-time.Second)
	v2EventMu.Unlock()
	SweepExpiredV2Events()
	if gotUUID != "node-timeout" || gotTask != "task-late" {
		t.Fatalf("expired exec handler uuid=%q task=%q", gotUUID, gotTask)
	}
	if events := TakeV2Events("node-timeout", nil, 8); len(events) != 0 {
		t.Fatalf("expired exec remained queued: %#v", events)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
