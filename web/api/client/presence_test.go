package client

import (
	"sync"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils/notifier"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/connection"
)

var presenceTestDBOnce sync.Once

func ensurePresenceTestDB(t *testing.T) {
	t.Helper()
	presenceTestDBOnce.Do(func() {
		if flags.DatabaseType == "" {
			flags.DatabaseType = "sqlite"
		}
		if flags.DatabaseFile == "" {
			flags.DatabaseFile = "file:v2_presence_test?mode=memory&cache=shared"
		}
		_ = dbcore.GetDBInstance()
	})
}

func TestHTTPPresenceExpirySkipsOfflineWhenWebSocketIsLive(t *testing.T) {
	ensurePresenceTestDB(t)
	uuid := "node-ws-live"
	t.Cleanup(func() {
		clearPostPresence(uuid)
		agent_runtime.DeleteConnectedClients(uuid)
		notifier.ForgetClient(uuid)
	})

	agent_runtime.SetConnectedClients(uuid, &connection.SafeConn{ID: 9})
	refreshPostPresence(uuid)

	postPresenceMu.Lock()
	entry := postPresenceStates[uuid]
	postPresenceMu.Unlock()
	if entry == nil {
		t.Fatal("expected HTTP presence session")
	}

	postPresenceExpired(uuid, entry.connID, entry.generation)
	if agent_runtime.GetConnectedClient(uuid) == nil {
		t.Fatal("WebSocket presence was cleared by HTTP expiry")
	}
	if !agent_runtime.IsPresent(uuid) {
		t.Fatal("node should stay online on WebSocket after HTTP presence expires")
	}
	if httpSessionGoneShouldNotify(uuid) {
		t.Fatal("HTTP expiry must not page offline while WebSocket is live")
	}
}

func TestHTTPPresenceExpiryNotifiesWhenWebSocketIsGone(t *testing.T) {
	uuid := "node-http-only"
	t.Cleanup(func() {
		clearPostPresence(uuid)
		agent_runtime.DeleteConnectedClients(uuid)
		notifier.ForgetClient(uuid)
	})
	if !httpSessionGoneShouldNotify(uuid) {
		t.Fatal("HTTP-only node should notify when presence expires")
	}
}

func TestAgentPullOnlyRefreshesPresenceOnHTTP(t *testing.T) {
	ensurePresenceTestDB(t)
	uuid := "node-pull-presence"
	t.Cleanup(func() {
		clearPostPresence(uuid)
		agent_runtime.DeleteConnectedClients(uuid)
		notifier.ForgetClient(uuid)
	})

	req := v2.Request{
		JSONRPC: v2.Version,
		ID:      "pull-1",
		Method:  v2.MethodAgentPull,
		Params:  v2.PullParams{Capabilities: []string{"exec"}},
	}

	// WebSocket path passes markPresence=false: must NOT start a 35s HTTP presence session.
	resp := handleV2RPCWithPresence(uuid, req, false, false)
	if resp.Error != nil {
		t.Fatalf("unexpected WS pull error: %+v", resp.Error)
	}
	postPresenceMu.Lock()
	wsEntry := postPresenceStates[uuid]
	postPresenceMu.Unlock()
	if wsEntry != nil || agent_runtime.IsPresent(uuid) {
		t.Fatalf("WebSocket agent.pull must not arm HTTP postPresence timer: entry=%v present=%v", wsEntry, agent_runtime.IsPresent(uuid))
	}

	// HTTP path passes markPresence=true: MUST start/refresh HTTP presence session.
	resp = handleV2RPCWithPresence(uuid, req, false, true)
	if resp.Error != nil {
		t.Fatalf("unexpected HTTP pull error: %+v", resp.Error)
	}
	postPresenceMu.Lock()
	httpEntry := postPresenceStates[uuid]
	postPresenceMu.Unlock()
	if httpEntry == nil || !agent_runtime.IsPresent(uuid) {
		t.Fatalf("HTTP agent.pull must refresh HTTP postPresence: entry=%v present=%v", httpEntry, agent_runtime.IsPresent(uuid))
	}
}

func TestWebSocketTakeoverAndLateHTTPExpiryStillNotifyOnRealCrash(t *testing.T) {
	ensurePresenceTestDB(t)
	db := dbcore.GetDBInstance()

	prevLive := notifier.ClientIsLive
	notifier.ClientIsLive = agent_runtime.IsPresent
	notifier.ResumeOfflineNotifications()
	if err := config.Set(config.NotificationEnabledKey, true); err != nil {
		t.Fatalf("enable notifications: %v", err)
	}
	t.Cleanup(func() {
		notifier.ClientIsLive = prevLive
	})

	waitLastNotified := func(uuid string) bool {
		deadline := time.Now().Add(2500 * time.Millisecond)
		for time.Now().Before(deadline) {
			var row models.OfflineNotification
			if err := db.Where("client = ?", uuid).First(&row).Error; err == nil && row.LastNotified != nil && !row.LastNotified.IsZero() {
				return true
			}
			time.Sleep(40 * time.Millisecond)
		}
		return false
	}

	// Case A: HTTP fallback -> upgrade to WebSocket -> node crashes within the old 35s HTTP TTL.
	uuidA := "node-http-to-ws-crash"
	now := time.Now().UTC()
	_ = db.Where("uuid = ?", uuidA).Delete(&models.Client{})
	_ = db.Where("client = ?", uuidA).Delete(&models.OfflineNotification{})
	if err := db.Create(&models.Client{UUID: uuidA, Token: "tok-a", Name: "node_a", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.OfflineNotification{Client: uuidA, Enable: true, GracePeriod: 1}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clearPostPresence(uuidA)
		agent_runtime.DeleteConnectedClients(uuidA)
		notifier.ForgetClient(uuidA)
		_ = db.Where("uuid = ?", uuidA).Delete(&models.Client{})
		_ = db.Where("client = ?", uuidA).Delete(&models.OfflineNotification{})
	})

	refreshPostPresence(uuidA)
	wsA := &connection.SafeConn{ID: connection.NextID()}
	agent_runtime.SetConnectedClients(uuidA, wsA)
	clearPostPresence(uuidA)
	notifierOnline(uuidA, wsA.ID)

	// Node crashes immediately (well inside the old 35s HTTP TTL).
	agent_runtime.DeleteClientConditionally(uuidA, wsA)
	if liveID, live := agent_runtime.ActiveConnectionID(uuidA); live {
		t.Fatalf("stale HTTP presence survived WebSocket takeover: liveID=%d", liveID)
	}
	notifierOffline(uuidA, wsA.ID)
	if !waitLastNotified(uuidA) {
		t.Fatal("offline notification was swallowed when node crashed shortly after HTTP->WS upgrade")
	}

	// Case B: WebSocket connects -> late in-flight HTTP packet creates postPresence -> HTTP expires while WS lives -> WS later crashes.
	uuidB := "node-late-http-then-ws-crash"
	_ = db.Where("uuid = ?", uuidB).Delete(&models.Client{})
	_ = db.Where("client = ?", uuidB).Delete(&models.OfflineNotification{})
	if err := db.Create(&models.Client{UUID: uuidB, Token: "tok-b", Name: "node_b", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.OfflineNotification{Client: uuidB, Enable: true, GracePeriod: 1}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clearPostPresence(uuidB)
		agent_runtime.DeleteConnectedClients(uuidB)
		notifier.ForgetClient(uuidB)
		_ = db.Where("uuid = ?", uuidB).Delete(&models.Client{})
		_ = db.Where("client = ?", uuidB).Delete(&models.OfflineNotification{})
	})

	wsB := &connection.SafeConn{ID: connection.NextID()}
	agent_runtime.SetConnectedClients(uuidB, wsB)
	clearPostPresence(uuidB)
	notifierOnline(uuidB, wsB.ID)

	// Late HTTP fallback packet arrives after WS connected.
	refreshPostPresence(uuidB)
	postPresenceMu.Lock()
	lateEntry := postPresenceStates[uuidB]
	postPresenceMu.Unlock()
	if lateEntry == nil {
		t.Fatal("expected late HTTP presence entry")
	}
	if lateEntry.timer != nil {
		lateEntry.timer.Stop()
	}

	// 35s later HTTP presence expires while wsB is still connected: must NOT notify offline, and must rebind ownership to wsB.
	postPresenceExpired(uuidB, lateEntry.connID, lateEntry.generation)
	if !agent_runtime.IsPresent(uuidB) {
		t.Fatal("wsB should still be online")
	}
	time.Sleep(1200 * time.Millisecond)
	var notifiedB models.OfflineNotification
	if err := db.Where("client = ?", uuidB).First(&notifiedB).Error; err != nil {
		t.Fatal(err)
	}
	if notifiedB.LastNotified != nil && !notifiedB.LastNotified.IsZero() {
		t.Fatal("HTTP expiry paged offline while WebSocket was still live")
	}

	// Later wsB genuinely crashes: offline alert MUST still fire.
	agent_runtime.DeleteClientConditionally(uuidB, wsB)
	if liveID, live := agent_runtime.ActiveConnectionID(uuidB); live {
		notifier.RebindOnlineConnection(uuidB, wsB.ID, liveID)
		t.Fatalf("unexpected surviving connection %d", liveID)
	}
	notifierOffline(uuidB, wsB.ID)
	if !waitLastNotified(uuidB) {
		t.Fatal("offline notification was swallowed when WS crashed after late HTTP presence expiry")
	}
}

func TestPanelRestartDoesNotPageNodesThatReconnectOnWebSocket(t *testing.T) {
	ensurePresenceTestDB(t)
	db := dbcore.GetDBInstance()
	prevLive := notifier.ClientIsLive
	notifier.ClientIsLive = agent_runtime.IsPresent
	if err := config.Set(config.NotificationEnabledKey, true); err != nil {
		t.Fatalf("enable notifications: %v", err)
	}
	t.Cleanup(func() {
		notifier.ClientIsLive = prevLive
		notifier.ResumeOfflineNotifications()
	})

	uuid := "node-panel-restart"
	now := time.Now().UTC()
	_ = db.Where("uuid = ?", uuid).Delete(&models.Client{})
	_ = db.Where("client = ?", uuid).Delete(&models.OfflineNotification{})
	if err := db.Create(&models.Client{UUID: uuid, Token: "tok-restart", Name: "restart_node", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.OfflineNotification{Client: uuid, Enable: true, GracePeriod: 1}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clearPostPresence(uuid)
		agent_runtime.DeleteConnectedClients(uuid)
		notifier.ForgetClient(uuid)
		_ = db.Where("uuid = ?", uuid).Delete(&models.Client{})
		_ = db.Where("client = ?", uuid).Delete(&models.OfflineNotification{})
	})

	lastNotified := func() bool {
		var row models.OfflineNotification
		if err := db.Where("client = ?", uuid).First(&row).Error; err != nil {
			return false
		}
		return row.LastNotified != nil && !row.LastNotified.IsZero()
	}

	// Old process: panel shutdown closes the WebSocket while the node is still alive.
	wsOld := &connection.SafeConn{ID: connection.NextID()}
	agent_runtime.SetConnectedClients(uuid, wsOld)
	notifierOnline(uuid, wsOld.ID)
	notifier.SuppressOfflineNotifications()
	agent_runtime.DeleteClientConditionally(uuid, wsOld)
	notifierOffline(uuid, wsOld.ID)
	time.Sleep(1200 * time.Millisecond)
	if lastNotified() {
		t.Fatal("panel shutdown paged a live node as offline")
	}

	// New process: HTTP fallback then WebSocket, then the leftover HTTP TTL expires.
	notifier.ResumeOfflineNotifications()
	notifier.ForgetClient(uuid)
	refreshPostPresence(uuid)
	wsNew := &connection.SafeConn{ID: connection.NextID()}
	agent_runtime.SetConnectedClients(uuid, wsNew)
	clearPostPresence(uuid)
	notifierOnline(uuid, wsNew.ID)
	refreshPostPresence(uuid) // late in-flight HTTP after WS
	postPresenceMu.Lock()
	late := postPresenceStates[uuid]
	postPresenceMu.Unlock()
	if late == nil {
		t.Fatal("expected late HTTP presence after reconnect")
	}
	if late.timer != nil {
		late.timer.Stop()
	}
	postPresenceExpired(uuid, late.connID, late.generation)
	time.Sleep(1200 * time.Millisecond)
	if lastNotified() {
		t.Fatal("reconnect HTTP expiry paged a node that was already back on WebSocket")
	}
	if !agent_runtime.IsPresent(uuid) {
		t.Fatal("reconnected WebSocket should keep the node online")
	}
}
