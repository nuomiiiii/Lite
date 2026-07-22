package notifier

import (
	"testing"
	"time"
)

func TestOnlineStateRecordedBeforeNotificationIsEnabled(t *testing.T) {
	state := &notificationState{isFirstConnection: true}
	if notify, duplicate := recordOnlineConnection(state, 42); notify || duplicate {
		t.Fatalf("first connection result = notify %v, duplicate %v", notify, duplicate)
	}

	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	if !beginOfflineGrace(state, 42, now) {
		t.Fatal("first disconnect after enabling notifications did not enter the grace period")
	}
	if !state.pendingOfflineSince.Equal(now) {
		t.Fatalf("pending offline time = %v, want %v", state.pendingOfflineSince, now)
	}
}

func TestOfflineStateRejectsStaleConnection(t *testing.T) {
	state := &notificationState{isFirstConnection: true}
	recordOnlineConnection(state, 100)
	recordOnlineConnection(state, 101)

	if beginOfflineGrace(state, 100, time.Now().UTC()) {
		t.Fatal("stale connection entered the offline grace period")
	}
	if !state.pendingOfflineSince.IsZero() {
		t.Fatalf("stale connection changed pending offline time: %v", state.pendingOfflineSince)
	}
}

func TestReconnectDuringGraceCancelsPreviousOfflineEvent(t *testing.T) {
	state := &notificationState{isFirstConnection: true}
	recordOnlineConnection(state, 200)
	if !beginOfflineGrace(state, 200, time.Now().UTC()) {
		t.Fatal("current connection did not enter the offline grace period")
	}

	if notify, duplicate := recordOnlineConnection(state, 201); notify || duplicate {
		t.Fatalf("grace-period reconnect result = notify %v, duplicate %v", notify, duplicate)
	}
	if !state.pendingOfflineSince.IsZero() {
		t.Fatalf("reconnect did not cancel pending offline event: %v", state.pendingOfflineSince)
	}
	if beginOfflineGrace(state, 200, time.Now().UTC()) {
		t.Fatal("old connection event was accepted after reconnect")
	}
	if !beginOfflineGrace(state, 201, time.Now().UTC()) {
		t.Fatal("current connection event was rejected after reconnect")
	}
}
