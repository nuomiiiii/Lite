package remote

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/metricstore"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/remotectl"
)

const (
	pendingSessionTTL         = 45 * time.Second
	remoteIdleTimeout         = 45 * time.Second
	remotePingInterval        = 15 * time.Second
	remoteMaxDuration         = 2 * time.Hour
	remoteReadLimit           = 2 << 20
	maxRemoteSessions         = 64
	maxRemoteSessionsPerLogin = 16
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

var (
	errRemoteSessionLimit = errors.New("too many active remote sessions")
	errLoginSessionLimit  = errors.New("too many remote sessions for this login")
)

func init() {
	remotectl.CloseLoginSessions = CloseLoginSessions
	remotectl.CloseUserSessions = CloseUserSessions
	remotectl.CloseAllSessions = CloseAllRemoteSessions
}

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
	return session.isStaleLocked(now)
}

func (session *remoteSession) isStaleLocked(now time.Time) bool {
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
)

func putSession(session *remoteSession) error {
	if session != nil {
		session.LoginSession = accounts.SessionLookupKey(session.LoginSession)
	}
	sessionsMu.Lock()
	now := time.Now()
	pruned := takeStaleSessionsLocked(now)
	blocked := metricstore.EntityWritesBlocked(session.UUID)
	loginCount := 0
	if !blocked {
		for _, existing := range sessions {
			if existing != nil && existing.LoginSession == session.LoginSession {
				loginCount++
			}
		}
	}
	overGlobal := len(sessions) >= maxRemoteSessions
	overLogin := loginCount >= maxRemoteSessionsPerLogin
	if !blocked && !overGlobal && !overLogin {
		sessions[session.ID] = session
	}
	sessionsMu.Unlock()
	closeTakenSessions(pruned)
	if blocked {
		return errors.New("client is being deleted")
	}
	if overGlobal {
		return errRemoteSessionLimit
	}
	if overLogin {
		return errLoginSessionLimit
	}
	return nil
}

func pruneStaleSessions(now time.Time) {
	sessionsMu.Lock()
	pruned := takeStaleSessionsLocked(now)
	sessionsMu.Unlock()
	closeTakenSessions(pruned)
}

func takeStaleSessionsLocked(now time.Time) []*remoteSession {
	pruned := make([]*remoteSession, 0)
	for id, session := range sessions {
		if session == nil || session.stale(now) {
			delete(sessions, id)
			if session != nil {
				pruned = append(pruned, session)
			}
		}
	}
	return pruned
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
	closeTakenSessions([]*remoteSession{session})
}

func closeTakenSessions(taken []*remoteSession) {
	for _, session := range taken {
		if session == nil {
			continue
		}
		agent_runtime.RemoveV2RemoteRequest(session.UUID, session.ID)
		session.mu.Lock()
		if session.closed {
			session.mu.Unlock()
			continue
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
}

func deleteOwnedSession(id, userUUID, loginSession string) bool {
	session := getSession(id)
	if session == nil {
		return true
	}
	session.mu.Lock()
	owned := !session.closed && session.UserUUID == userUUID && session.LoginSession == accounts.SessionLookupKey(loginSession)
	session.mu.Unlock()
	if !owned {
		return false
	}
	deleteSession(id)
	return true
}

func CloseClientSessions(uuid string) {
	closeMatching(func(session *remoteSession) bool {
		return session.UUID == uuid
	})
}

func CloseLoginSessions(loginSession string) {
	if loginSession == "" {
		return
	}
	closeMatching(func(session *remoteSession) bool {
		return session.LoginSession == accounts.SessionLookupKey(loginSession)
	})
}

func CloseUserSessions(userUUID string) {
	if userUUID == "" {
		return
	}
	closeMatching(func(session *remoteSession) bool {
		return session.UserUUID == userUUID
	})
}

func CloseAllRemoteSessions() {
	closeMatching(func(*remoteSession) bool { return true })
}

func closeMatching(match func(*remoteSession) bool) {
	sessionsMu.RLock()
	ids := make([]string, 0)
	for id, session := range sessions {
		if session != nil && match(session) {
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

func loginStillValid(userUUID, loginSession string) bool {
	return accounts.SessionStillValid(userUUID, loginSession)
}
