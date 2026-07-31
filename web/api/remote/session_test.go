package remote

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
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

func TestOwnedRemoteSessionsCanBeReleasedAcrossConsecutiveLaunches(t *testing.T) {
	replaceRemoteSessions(t, make(map[string]*remoteSession))
	now := time.Now()
	for attempt := 1; attempt <= 3; attempt++ {
		id := string(rune('a' + attempt))
		session := &remoteSession{
			ID:           id,
			UUID:         "node-a",
			UserUUID:     "user-a",
			LoginSession: "login-a",
			ExpiresAt:    now.Add(time.Minute),
			LastActivity: now,
		}
		if err := putSession(session); err != nil {
			t.Fatalf("launch %d: putSession error = %v", attempt, err)
		}
		if !deleteOwnedSession(id, "user-a", "login-a") {
			t.Fatalf("launch %d: owner could not release session", attempt)
		}
		if getSession(id) != nil {
			t.Fatalf("launch %d: released session remains registered", attempt)
		}
	}
}

func TestConcurrentRemotePagesKeepIndependentSessions(t *testing.T) {
	replaceRemoteSessions(t, make(map[string]*remoteSession))
	const pageCount = 32
	now := time.Now()
	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, pageCount)

	for page := 0; page < pageCount; page++ {
		page := page
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			id := fmt.Sprintf("page-%02d", page)
			if err := putSession(&remoteSession{
				ID:           id,
				UUID:         fmt.Sprintf("node-%02d", page),
				UserUUID:     "user-a",
				LoginSession: "login-a",
				ExpiresAt:    now.Add(time.Minute),
				LastActivity: now,
			}); err != nil {
				errorsCh <- fmt.Errorf("%s: %w", id, err)
			}
		}()
	}
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}

	sessionsMu.RLock()
	registered := len(sessions)
	sessionsMu.RUnlock()
	if registered != pageCount {
		t.Fatalf("registered session count = %d, want %d", registered, pageCount)
	}

	if !deleteOwnedSession("page-07", "user-a", "login-a") {
		t.Fatal("owner could not release one independent page session")
	}
	if getSession("page-07") != nil {
		t.Fatal("released page session remains registered")
	}
	for page := 0; page < pageCount; page++ {
		id := fmt.Sprintf("page-%02d", page)
		if id != "page-07" && getSession(id) == nil {
			t.Fatalf("closing page-07 removed independent session %s", id)
		}
	}

	if err := putSession(&remoteSession{
		ID:           "replacement-page",
		UUID:         "replacement-node",
		UserUUID:     "user-a",
		LoginSession: "login-a",
		ExpiresAt:    now.Add(time.Minute),
		LastActivity: now,
	}); err != nil {
		t.Fatalf("released capacity could not be reused: %v", err)
	}
}

func TestRemoteSessionCannotBeReleasedByAnotherLogin(t *testing.T) {
	replaceRemoteSessions(t, map[string]*remoteSession{
		"session-a": {
			ID:           "session-a",
			UserUUID:     "user-a",
			LoginSession: "login-a",
			ExpiresAt:    time.Now().Add(time.Minute),
		},
	})

	if deleteOwnedSession("session-a", "user-a", "login-b") {
		t.Fatal("another login released the remote session")
	}
	if getSession("session-a") == nil {
		t.Fatal("unauthorized release removed the remote session")
	}
}
