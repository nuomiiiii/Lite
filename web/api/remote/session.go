package remote

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pendingSessionTTL = 45 * time.Second
	remoteStepUpTTL   = 10 * time.Minute
	remoteMaxDuration = 2 * time.Hour
	remoteReadLimit   = 2 << 20
	maxRemoteSessions = 16
)

type remoteSession struct {
	mu            sync.Mutex
	forwardOnce   sync.Once
	ID            string
	UUID          string
	UserUUID      string
	LoginSession  string
	RequesterIP   string
	BrowserTicket string
	AgentTicket   string
	Browser       *websocket.Conn
	Agent         *websocket.Conn
	CreatedAt     time.Time
	ExpiresAt     time.Time
	StartedAt     time.Time
	closed        bool
}

func (session *remoteSession) attachBrowser(ticket string, connection *websocket.Conn, now time.Time) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	valid := !session.closed && session.Browser == nil && now.Before(session.ExpiresAt) &&
		ticketsEqual(session.BrowserTicket, ticket)
	if valid {
		session.BrowserTicket = ""
		session.Browser = connection
	}
	return valid
}

func (session *remoteSession) canAttachAgent(clientUUID, ticket string, now time.Time) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return !session.closed && session.UUID == clientUUID && session.Browser != nil &&
		session.Agent == nil && now.Before(session.ExpiresAt) && ticketsEqual(session.AgentTicket, ticket)
}

func (session *remoteSession) attachAgent(clientUUID, ticket string, connection *websocket.Conn, now time.Time) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	valid := !session.closed && session.UUID == clientUUID && session.Browser != nil &&
		session.Agent == nil && now.Before(session.ExpiresAt) && ticketsEqual(session.AgentTicket, ticket)
	if valid {
		session.AgentTicket = ""
		session.Agent = connection
		session.StartedAt = now
	}
	return valid
}

func (session *remoteSession) pendingAgentTicket() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.AgentTicket
}

var (
	sessionsMu sync.RWMutex
	sessions   = make(map[string]*remoteSession)
	stepUpMu   sync.Mutex
	stepUps    = make(map[string]time.Time)
)

func putSession(session *remoteSession) error {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if len(sessions) >= maxRemoteSessions {
		return errors.New("too many active remote sessions")
	}
	sessions[session.ID] = session
	return nil
}

func getSession(id string) *remoteSession {
	sessionsMu.RLock()
	defer sessionsMu.RUnlock()
	return sessions[id]
}

func deleteSession(id string) {
	sessionsMu.Lock()
	session := sessions[id]
	delete(sessions, id)
	sessionsMu.Unlock()
	if session == nil {
		return
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return
	}
	session.closed = true
	browser := session.Browser
	agent := session.Agent
	session.mu.Unlock()
	if browser != nil {
		_ = browser.Close()
	}
	if agent != nil {
		_ = agent.Close()
	}
}

func CloseClientSessions(uuid string) {
	sessionsMu.RLock()
	ids := make([]string, 0)
	for id, session := range sessions {
		if session.UUID == uuid {
			ids = append(ids, id)
		}
	}
	sessionsMu.RUnlock()
	for _, id := range ids {
		deleteSession(id)
	}
}

func ticketsEqual(left, right string) bool {
	if left == "" || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func hasFreshStepUp(loginSession string) bool {
	if loginSession == "" {
		return false
	}
	now := time.Now()
	stepUpMu.Lock()
	defer stepUpMu.Unlock()
	for token, expiresAt := range stepUps {
		if !expiresAt.After(now) {
			delete(stepUps, token)
		}
	}
	return stepUps[loginSession].After(now)
}

func rememberStepUp(loginSession string) {
	if loginSession == "" {
		return
	}
	stepUpMu.Lock()
	stepUps[loginSession] = time.Now().Add(remoteStepUpTTL)
	stepUpMu.Unlock()
}
