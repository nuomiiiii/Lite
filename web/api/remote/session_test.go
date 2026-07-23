package remote

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
	sessionsMu.Lock()
	original := sessions
	sessions = map[string]*remoteSession{
		"a-1": {ID: "a-1", UUID: "node-a"},
		"a-2": {ID: "a-2", UUID: "node-a"},
		"b-1": {ID: "b-1", UUID: "node-b"},
	}
	sessionsMu.Unlock()
	t.Cleanup(func() {
		sessionsMu.Lock()
		sessions = original
		sessionsMu.Unlock()
	})

	CloseClientSessions("node-a")
	if getSession("a-1") != nil || getSession("a-2") != nil {
		t.Fatal("protected node sessions remain active")
	}
	if getSession("b-1") == nil {
		t.Fatal("unrelated node session was closed")
	}
}
