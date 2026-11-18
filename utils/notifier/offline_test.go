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

func TestFormatChineseDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0秒"},
		{21 * time.Second, "21秒"},
		{35*time.Minute + 21*time.Second, "35分钟21秒"},
		{time.Hour + 35*time.Minute + 21*time.Second, "1小时35分钟21秒"},
		{2*time.Hour + 5*time.Second, "2小时5秒"},
		{90 * time.Second, "1分钟30秒"},
		{-time.Second, "0秒"},
	}
	for _, tc := range cases {
		if got := formatChineseDuration(tc.d); got != tc.want {
			t.Errorf("formatChineseDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestOnlineRecoveryMessage(t *testing.T) {
	offline := time.Date(2026, 10, 16, 6, 24, 3, 0, time.UTC)
	online := offline.Add(time.Hour + 35*time.Minute + 21*time.Second)
	got := onlineRecoveryMessage(offline, online)
	if got != "离线时间：1小时35分钟21秒" {
		t.Fatalf("onlineRecoveryMessage = %q", got)
	}
	if onlineRecoveryMessage(time.Time{}, online) != "" {
		t.Fatal("zero offline time should omit duration")
	}
	if onlineRecoveryMessage(online, offline) != "" {
		t.Fatal("future offline time should omit duration")
	}
}

func TestSkipOfflineSendWhenLiveOrShuttingDown(t *testing.T) {
	t.Cleanup(func() {
		suppressOffline.Store(false)
		ClientIsLive = nil
	})
	if skipOfflineSend("n1") {
		t.Fatal("idle client should still notify")
	}
	ClientIsLive = func(id string) bool { return id == "n1" }
	if !skipOfflineSend("n1") {
		t.Fatal("live client should not notify")
	}
	if skipOfflineSend("n2") {
		t.Fatal("unrelated client should still notify")
	}
	ClientIsLive = nil
	SuppressOfflineNotifications()
	if !skipOfflineSend("n2") {
		t.Fatal("panel shutdown should not notify")
	}
	ResumeOfflineNotifications()
	if skipOfflineSend("n2") {
		t.Fatal("resuming notifications should clear shutdown suppression")
	}
}

func TestRebindOnlineConnectionPreservesLaterDisconnectAlert(t *testing.T) {
	const id = "rebind-client"
	t.Cleanup(func() { ForgetClient(id) })

	// WebSocket (2000) connects first, then a late HTTP packet registers (3000).
	state := getOrInitState(id)
	recordOnlineConnection(state, 2000)
	recordOnlineConnection(state, 3000)

	// HTTP (3000) expires while WebSocket (2000) is still live -> rebind back to 2000.
	RebindOnlineConnection(id, 3000, 2000)

	if !beginOfflineGrace(state, 2000, time.Now().UTC()) {
		t.Fatal("rebound WebSocket disconnect was rejected as stale")
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
