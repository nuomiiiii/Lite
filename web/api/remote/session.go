package remote

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/database/metricstore"
)

const (
	pendingSessionTTL  = 45 * time.Second
	remoteIdleTimeout  = 45 * time.Second
	remotePingInterval = 15 * time.Second
	remoteStepUpTTL    = 10 * time.Minute
	remoteMaxDuration  = 2 * time.Hour
	remoteReadLimit    = 2 << 20
	maxRemoteSessions  = 64
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
	LastActivity  time.Time
	closed        bool
}

var errRemoteSessionLimit = errors.New("too many active remote sessions")

func (session *remoteSession) attachBrowser(ticket string, connection *websocket.Conn, now time.Time) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	valid := !session.closed && session.Browser == nil && now.Before(session.ExpiresAt) &&
		ticketsEqual(session.BrowserTicket, ticket)
	if valid {
		session.BrowserTicket = ""
		session.Browser = connection
		session.LastActivity = now
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
		session.LastActivity = now
	}
	return valid
}

func (session *remoteSession) touch(now time.Time) {
	session.mu.Lock()
	if !session.closed {
		session.LastActivity = now
	}
	session.mu.Unlock()
}

func (session *remoteSession) stale(now time.Time) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return true
	}
	if session.StartedAt.IsZero() {
		return !now.Before(session.ExpiresAt)
	}
	lastActivity := session.LastActivity
	if lastActivity.IsZero() {
		lastActivity = session.StartedAt
	}
	return now.Sub(lastActivity) > remoteIdleTimeout || now.Sub(session.StartedAt) > remoteMaxDuration
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
	pruneStaleSessions(time.Now())
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	if metricstore.EntityWritesBlocked(session.UUID) {
		return errors.New("client is being deleted")
	}
	if len(sessions) >= maxRemoteSessions {
		return errRemoteSessionLimit
	}
	sessions[session.ID] = session
	return nil
}

func pruneStaleSessions(now time.Time) {
	sessionsMu.RLock()
	ids := make([]string, 0)
	for id, session := range sessions {
		if session == nil || session.stale(now) {
			ids = append(ids, id)
		}
	}
	sessionsMu.RUnlock()
	for _, id := range ids {
		deleteSession(id)
	}
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

func deleteOwnedSession(id, userUUID, loginSession string) bool {
	session := getSession(id)
	if session == nil {
		return true
	}
	session.mu.Lock()
	owned := !session.closed && session.UserUUID == userUUID && session.LoginSession == loginSession
	session.mu.Unlock()
	if !owned {
		return false
	}
	deleteSession(id)
	return true
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
