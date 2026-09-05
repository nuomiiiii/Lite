package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

const (
	v2EventQueueLimit     = 128
	v2EventTTL            = 5 * time.Minute
	v2PingEventTTL        = 3 * time.Second
	pendingRemoteEventTTL = 45 * time.Second
)

type v2EventQueue struct {
	events []v2.Event
	signal chan struct{}
}

var (
	v2EventMu     sync.Mutex
	v2EventQueues = make(map[string]*v2EventQueue)
)

func getV2EventQueueLocked(uuid string) *v2EventQueue {
	q := v2EventQueues[uuid]
	if q == nil {
		q = &v2EventQueue{signal: make(chan struct{})}
		v2EventQueues[uuid] = q
	}
	return q
}

func DispatchV2Event(uuid, method string, params any) bool {
	if conn := GetConnectedClient(uuid); conn != nil {
		payload := v2.Request{JSONRPC: v2.Version, Method: method, Params: params}
		if conn.WriteJSON(payload) == nil {
			return true
		}
	}
	event := EnqueueV2Event(uuid, method, params)
	return event.ID != ""
}

func DispatchV2ExecEvent(uuid string, params v2.ExecParams) (queued bool, notified bool) {
	event := EnqueueV2Event(uuid, v2.MethodAgentExec, params)
	if event.ID == "" {
		return false, false
	}
	if conn := GetConnectedClient(uuid); conn != nil {
		payload := v2.Request{JSONRPC: v2.Version, Method: event.Method, Params: event.Params, ID: event.ID}
		if conn.WriteJSON(payload) == nil {
			return true, true
		}
	}
	return true, false
}

func DispatchV2Config(uuid string, params v2.ConfigParams) (v2.Event, bool, bool) {
	event := EnqueueV2Event(uuid, v2.MethodAgentConfig, params)
	if event.ID == "" {
		return event, false, true
	}
	if conn := GetConnectedClient(uuid); conn != nil {
		payload := v2.Request{JSONRPC: v2.Version, Method: event.Method, Params: event.Params, ID: event.ID}
		if conn.WriteJSON(payload) == nil {
			return event, true, true
		}
	}
	return event, false, true
}

func DispatchPing(uuid string, params v2.PingParams) bool {
	return DispatchV2Event(uuid, v2.MethodAgentPing, params)
}

func IsAgentOnline(uuid string) bool {
	return IsPresent(uuid)
}

func EnqueueV2Event(uuid, method string, params any) v2.Event {
	if metricstore.EntityWritesBlocked(uuid) {
		return v2.Event{}
	}
	now := time.Now().UTC()
	ttl := v2EventTTL
	if method == v2.MethodAgentPing {
		ttl = v2PingEventTTL
	} else if method == v2.MethodAgentRoute {
		ttl = 2 * time.Minute
	} else if method == v2.MethodAgentRemote {
		ttl = pendingRemoteEventTTL
	}
	event := v2.Event{
		ID:        newV2EventID(),
		Method:    method,
		Params:    params,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}

	v2EventMu.Lock()
	q := getV2EventQueueLocked(uuid)
	expired := pruneExpiredV2EventsLocked(q)
	coalesceV2EventLocked(q, event)
	if len(q.events) >= v2EventQueueLimit {
		v2EventMu.Unlock()
		handleExpiredV2Events(uuid, expired)
		return v2.Event{}
	}
	q.events = append(q.events, event)
	close(q.signal)
	q.signal = make(chan struct{})
	v2EventMu.Unlock()
	handleExpiredV2Events(uuid, expired)

	return event
}

func newV2EventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func coalesceV2EventLocked(q *v2EventQueue, event v2.Event) {
	key := v2EventCoalesceKey(event)
	if key == "" {
		return
	}
	filtered := q.events[:0]
	for _, existing := range q.events {
		if v2EventCoalesceKey(existing) != key {
			filtered = append(filtered, existing)
		}
	}
	q.events = filtered
}

func v2EventCoalesceKey(event v2.Event) string {
	if event.Method == v2.MethodAgentConfig {
		return v2.MethodAgentConfig
	}
	if event.Method != v2.MethodAgentPing && event.Method != v2.MethodAgentRoute {
		return ""
	}
	if event.Method == v2.MethodAgentRoute {
		var params v2.RouteParams
		if err := bindV2EventParams(event.Params, &params); err != nil || params.TaskID == 0 {
			return ""
		}
		return fmt.Sprintf("%s:%d", event.Method, params.TaskID)
	}
	var params v2.PingParams
	if err := bindV2EventParams(event.Params, &params); err != nil || params.TaskID == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", event.Method, params.TaskID)
}

func bindV2EventParams(raw any, target any) error {
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

func ackV2EventsLocked(q *v2EventQueue, ackIDs []string) {
	if len(ackIDs) == 0 || len(q.events) == 0 {
		return
	}
	acked := make(map[string]struct{}, len(ackIDs))
	for _, id := range ackIDs {
		acked[id] = struct{}{}
	}
	filtered := q.events[:0]
	for _, event := range q.events {
		if _, ok := acked[event.ID]; !ok {
			filtered = append(filtered, event)
		}
	}
	q.events = filtered
}

func pruneExpiredV2EventsLocked(q *v2EventQueue) []v2.Event {
	if len(q.events) == 0 {
		return nil
	}
	now := time.Now().UTC()
	filtered := q.events[:0]
	var expired []v2.Event
	for _, event := range q.events {
		if event.ExpiresAt.IsZero() || event.ExpiresAt.After(now) {
			filtered = append(filtered, event)
			continue
		}
		expired = append(expired, event)
	}
	q.events = filtered
	return expired
}

var expiredV2ExecHandler func(uuid, taskID string)

func SetExpiredV2ExecHandler(fn func(uuid, taskID string)) {
	expiredV2ExecHandler = fn
}

func handleExpiredV2Events(uuid string, events []v2.Event) {
	if expiredV2ExecHandler == nil || uuid == "" || len(events) == 0 {
		return
	}
	for _, event := range events {
		if event.Method != v2.MethodAgentExec {
			continue
		}
		var params v2.ExecParams
		if err := bindV2EventParams(event.Params, &params); err != nil || params.TaskID == "" {
			continue
		}
		expiredV2ExecHandler(uuid, params.TaskID)
	}
}

func SweepExpiredV2Events() {
	v2EventMu.Lock()
	type expiredQueue struct {
		uuid   string
		events []v2.Event
	}
	var expired []expiredQueue
	for uuid, q := range v2EventQueues {
		if q == nil {
			continue
		}
		events := pruneExpiredV2EventsLocked(q)
		if len(events) > 0 {
			expired = append(expired, expiredQueue{uuid: uuid, events: events})
		}
	}
	v2EventMu.Unlock()
	for _, item := range expired {
		handleExpiredV2Events(item.uuid, item.events)
	}
}

func TakeV2Events(uuid string, ackIDs []string, limit int) []v2.Event {
	v2EventMu.Lock()
	q := v2EventQueues[uuid]
	if q == nil {
		v2EventMu.Unlock()
		return nil
	}
	ackV2EventsLocked(q, ackIDs)
	expired := pruneExpiredV2EventsLocked(q)
	events := takeV2EventsLocked(q, limit)
	v2EventMu.Unlock()
	handleExpiredV2Events(uuid, expired)
	return events
}

func AckV2Events(uuid string, ackIDs []string) {
	if len(ackIDs) == 0 {
		return
	}
	v2EventMu.Lock()
	defer v2EventMu.Unlock()

	q := v2EventQueues[uuid]
	if q == nil {
		return
	}
	ackV2EventsLocked(q, ackIDs)
}

type RemovedV2Event struct {
	UUID  string
	Event v2.Event
}

func ExecTaskID(event v2.Event) string {
	var params v2.ExecParams
	if err := bindV2EventParams(event.Params, &params); err != nil {
		return ""
	}
	return params.TaskID
}

func RemoveV2EventsByMethods(uuid string, methods ...string) {
	if len(methods) == 0 {
		return
	}
	blocked := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		blocked[method] = struct{}{}
	}
	v2EventMu.Lock()
	defer v2EventMu.Unlock()
	q := v2EventQueues[uuid]
	if q == nil {
		return
	}
	filtered := q.events[:0]
	for _, event := range q.events {
		if _, remove := blocked[event.Method]; !remove {
			filtered = append(filtered, event)
		}
	}
	q.events = filtered
}

func RemoveAllV2EventsByMethods(methods ...string) []RemovedV2Event {
	if len(methods) == 0 {
		return nil
	}
	blocked := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		blocked[method] = struct{}{}
	}
	v2EventMu.Lock()
	var removed []RemovedV2Event
	for uuid, q := range v2EventQueues {
		if q == nil {
			continue
		}
		filtered := q.events[:0]
		for _, event := range q.events {
			if _, remove := blocked[event.Method]; remove {
				removed = append(removed, RemovedV2Event{UUID: uuid, Event: event})
				continue
			}
			filtered = append(filtered, event)
		}
		q.events = filtered
	}
	v2EventMu.Unlock()
	return removed
}

func RemoveV2RemoteRequest(uuid, requestID string) {
	if uuid == "" || requestID == "" {
		return
	}
	v2EventMu.Lock()
	defer v2EventMu.Unlock()
	q := v2EventQueues[uuid]
	if q == nil {
		return
	}
	filtered := q.events[:0]
	for _, event := range q.events {
		if event.Method == v2.MethodAgentRemote && v2RemoteRequestID(event) == requestID {
			continue
		}
		filtered = append(filtered, event)
	}
	q.events = filtered
}

func v2RemoteRequestID(event v2.Event) string {
	var params v2.RemoteRequestParams
	if err := bindV2EventParams(event.Params, &params); err != nil {
		return ""
	}
	return params.RequestID
}

func RemoveV2EventQueue(uuid string) {
	v2EventMu.Lock()
	q := v2EventQueues[uuid]
	delete(v2EventQueues, uuid)
	if q != nil {
		close(q.signal)
	}
	v2EventMu.Unlock()
}

func takeV2EventsLocked(q *v2EventQueue, limit int) []v2.Event {
	if limit <= 0 || limit > len(q.events) {
		limit = len(q.events)
	}
	events := make([]v2.Event, limit)
	copy(events, q.events[:limit])
	return events
}

func WaitV2Events(uuid string, ackIDs []string, timeout time.Duration) []v2.Event {
	if metricstore.EntityWritesBlocked(uuid) {
		return nil
	}
	v2EventMu.Lock()
	q := getV2EventQueueLocked(uuid)
	ackV2EventsLocked(q, ackIDs)
	expired := pruneExpiredV2EventsLocked(q)
	events := takeV2EventsLocked(q, v2EventQueueLimit)
	if len(events) > 0 || timeout <= 0 {
		v2EventMu.Unlock()
		handleExpiredV2Events(uuid, expired)
		return events
	}
	signal := q.signal
	v2EventMu.Unlock()
	handleExpiredV2Events(uuid, expired)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
	}
	return TakeV2Events(uuid, nil, v2EventQueueLimit)
}
