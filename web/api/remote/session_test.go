package remote

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func replaceRemoteSessions(t *testing.T, replacement map[string]*remoteSession) {
	t.Helper()
	sessionsMu.Lock()
	original := sessions
	sessions = replacement
	sessionsMu.Unlock()
	t.Cleanup(func() {
		sessionsMu.Lock()
		sessions = original
		sessionsMu.Unlock()
	})
}

func TestBrowserAuthorizationRequiresSessionAndTicket(t *testing.T) {
	for _, authorization := range []browserAuthorization{
		{Type: "auth", SessionID: "session"},
		{Type: "auth", Ticket: "ticket"},
		{Type: "heartbeat", SessionID: "session", Ticket: "ticket"},
	} {
		if authorization.valid() {
			t.Fatalf("incomplete browser authorization was accepted: %+v", authorization)
		}
	}
	if !(browserAuthorization{Type: "auth", SessionID: "session", Ticket: "ticket"}).valid() {
		t.Fatal("complete browser authorization was rejected")
	}
}

func TestAgentRemoteSessionIDIgnoresQueryParameter(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/clients/remote?id=query-session", nil)
	if got := agentRemoteSessionID(context); got != "" {
		t.Fatalf("query session identifier was accepted: %q", got)
	}
	context.Request.Header.Set("X-Komari-Remote-Session", "header-session")
	if got := agentRemoteSessionID(context); got != "header-session" {
		t.Fatalf("header session identifier was rejected: %q", got)
	}
}

func TestBrowserAndAgentTicketsAreSingleUse(t *testing.T) {
	now := time.Now()
	session := &remoteSession{
		UUID:          "node-a",
		BrowserTicket: "browser-ticket",
		AgentTicket:   "agent-ticket",
		ExpiresAt:     now.Add(time.Minute),
	}
	browser := &websocket.Conn{}
	agent := &websocket.Conn{}
	if !session.attachBrowser("browser-ticket", browser, now) {
		t.Fatal("valid browser ticket was rejected")
	}
	if session.attachBrowser("browser-ticket", &websocket.Conn{}, now) {
		t.Fatal("browser ticket replay was accepted")
	}
	if !session.attachAgent("node-a", "agent-ticket", agent, now) {
		t.Fatal("valid agent ticket was rejected")
	}
	if session.attachAgent("node-a", "agent-ticket", &websocket.Conn{}, now) {
		t.Fatal("agent ticket replay was accepted")
	}
}

func TestAgentTicketIsBoundToNodeAndExpiry(t *testing.T) {
	now := time.Now()
	session := &remoteSession{
		UUID:        "node-a",
		AgentTicket: "agent-ticket",
		Browser:     &websocket.Conn{},
		ExpiresAt:   now.Add(time.Minute),
	}
	if session.canAttachAgent("node-b", "agent-ticket", now) {
		t.Fatal("cross-node agent ticket was accepted")
	}
	if session.attachAgent("node-b", "agent-ticket", &websocket.Conn{}, now) {
		t.Fatal("cross-node agent attached")
	}
	if session.canAttachAgent("node-a", "agent-ticket", now.Add(2*time.Minute)) {
		t.Fatal("expired agent ticket was accepted")
	}
	if session.attachAgent("node-a", "agent-ticket", &websocket.Conn{}, now.Add(2*time.Minute)) {
		t.Fatal("expired agent attached")
	}
}

func TestCloseClientSessionsOnlyClosesSelectedNode(t *testing.T) {
	replaceRemoteSessions(t, map[string]*remoteSession{
		"a-1": {ID: "a-1", UUID: "node-a"},
		"a-2": {ID: "a-2", UUID: "node-a"},
		"b-1": {ID: "b-1", UUID: "node-b"},
	})

	CloseClientSessions("node-a")
	if getSession("a-1") != nil || getSession("a-2") != nil {
		t.Fatal("protected node sessions remain active")
	}
	if getSession("b-1") == nil {
		t.Fatal("unrelated node session was closed")
	}
}

func TestRemoteHeartbeatIsConsumedByServer(t *testing.T) {
	if !isRemoteHeartbeat(websocket.TextMessage, []byte(`{"type":"heartbeat","timestamp":123}`)) {
		t.Fatal("valid browser heartbeat was not recognized")
	}
	if isRemoteHeartbeat(websocket.BinaryMessage, []byte(`{"type":"heartbeat"}`)) {
		t.Fatal("binary terminal data was treated as a heartbeat")
	}
	if isRemoteHeartbeat(websocket.TextMessage, []byte(`{"type":"resize"}`)) {
		t.Fatal("terminal control message was treated as a heartbeat")
	}
}

func TestPruneStaleRemoteSessionsKeepsLiveSessions(t *testing.T) {
	now := time.Now()
	replaceRemoteSessions(t, map[string]*remoteSession{
		"pending-expired": {
			ID: "pending-expired", ExpiresAt: now.Add(-time.Second), LastActivity: now.Add(-time.Second),
		},
		"connected-stale": {
			ID: "connected-stale", ExpiresAt: now.Add(time.Minute), StartedAt: now.Add(-time.Minute), LastActivity: now.Add(-remoteIdleTimeout - time.Second),
		},
		"connected-live": {
			ID: "connected-live", ExpiresAt: now.Add(time.Minute), StartedAt: now.Add(-time.Minute), LastActivity: now,
		},
	})

	pruneStaleSessions(now)
	if getSession("pending-expired") != nil || getSession("connected-stale") != nil {
		t.Fatal("stale remote sessions were not removed")
	}
	if getSession("connected-live") == nil {
		t.Fatal("live remote session was removed")
	}
}

func TestPutSessionReturnsTypedLimitError(t *testing.T) {
	now := time.Now()
	full := make(map[string]*remoteSession, maxRemoteSessions)
	for index := 0; index < maxRemoteSessions; index++ {
		id := string(rune('a' + index))
		full[id] = &remoteSession{ID: id, ExpiresAt: now.Add(time.Minute), LastActivity: now}
	}
	replaceRemoteSessions(t, full)

	err := putSession(&remoteSession{ID: "overflow", ExpiresAt: now.Add(time.Minute), LastActivity: now})
	if !errors.Is(err, errRemoteSessionLimit) {
		t.Fatalf("putSession error=%v, want remote session limit", err)
	}
}
