package remotectl

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
)

const (
	ScopeRemote     = "remote"
	ScopeExec       = "exec"
	GrantTTL        = 10 * time.Minute
	grantSecretSize = 32
)

func init() {
	accounts.OnUserSecurityChanged = RevokeUser
}

var (
	ErrGrantRequired   = errors.New("remote grant is required")
	ErrGrantInvalid    = errors.New("remote grant is invalid")
	ErrGrantExpired    = errors.New("remote grant has expired")
	ErrGrantScope      = errors.New("remote grant does not match this page")
	ErrGrantPrincipal  = errors.New("remote grant does not match this login")
	ErrGrantWorkspace  = errors.New("remote grant does not match this workspace")
	ErrAPIKeyForbidden = errors.New("API keys cannot authorize remote management")
)

type storedGrant struct {
	hash         [32]byte
	userUUID     string
	loginSession string
	scope        string
	pageID       string
	expiresAt    time.Time
}

var (
	grantMu sync.Mutex
	grants  = make(map[string]storedGrant) // keyed by hex(hash)
)

func IssueGrant(userUUID, loginSession, scope, pageID string) (string, time.Time, error) {
	return issueGrant(userUUID, loginSession, scope, pageID, time.Now().Add(GrantTTL))
}

func issueGrant(userUUID, loginSession, scope, pageID string, expires time.Time) (string, time.Time, error) {
	if userUUID == "" || loginSession == "" {
		return "", time.Time{}, ErrGrantPrincipal
	}
	loginSession = accounts.SessionLookupKey(loginSession)
	if scope != ScopeRemote && scope != ScopeExec {
		return "", time.Time{}, ErrGrantScope
	}
	if scope == ScopeRemote && pageID == "" {
		return "", time.Time{}, ErrGrantWorkspace
	}
	if !expires.After(time.Now()) {
		return "", time.Time{}, ErrGrantExpired
	}
	var secret [grantSecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", time.Time{}, err
	}
	plain := hex.EncodeToString(secret[:])
	sum := sha256.Sum256([]byte(plain))
	grantMu.Lock()
	pruneGrantsLocked(time.Now())
	grants[hex.EncodeToString(sum[:])] = storedGrant{
		hash:         sum,
		userUUID:     userUUID,
		loginSession: loginSession,
		scope:        scope,
		pageID:       pageID,
		expiresAt:    expires,
	}
	grantMu.Unlock()
	return plain, expires, nil
}

func ConsumeGrant(plain, userUUID, loginSession, scope, pageID string) error {
	_, err := lookupGrant(plain, userUUID, loginSession, scope, pageID, false)
	return err
}

func TakeExecGrant(plain, userUUID, loginSession, pageID string) (time.Time, error) {
	stored, err := lookupGrant(plain, userUUID, loginSession, ScopeExec, pageID, true)
	if err != nil {
		return time.Time{}, err
	}
	return stored.expiresAt, nil
}

func RotateExecGrant(userUUID, loginSession, pageID string, expires time.Time) (string, time.Time, error) {
	return issueGrant(userUUID, loginSession, ScopeExec, pageID, expires)
}

func lookupGrant(plain, userUUID, loginSession, scope, pageID string, consume bool) (storedGrant, error) {
	if plain == "" {
		return storedGrant{}, ErrGrantRequired
	}
	sum := sha256.Sum256([]byte(plain))
	key := hex.EncodeToString(sum[:])
	now := time.Now()
	grantMu.Lock()
	defer grantMu.Unlock()
	pruneGrantsLocked(now)
	stored, ok := grants[key]
	if !ok {
		return storedGrant{}, ErrGrantInvalid
	}
	if subtle.ConstantTimeCompare(stored.hash[:], sum[:]) != 1 {
		return storedGrant{}, ErrGrantInvalid
	}
	if stored.userUUID != userUUID || stored.loginSession != accounts.SessionLookupKey(loginSession) {
		return storedGrant{}, ErrGrantPrincipal
	}
	if stored.scope != scope {
		return storedGrant{}, ErrGrantScope
	}
	if stored.pageID != "" && stored.pageID != pageID {
		return storedGrant{}, ErrGrantWorkspace
	}
	if !stored.expiresAt.After(now) {
		delete(grants, key)
		return storedGrant{}, ErrGrantExpired
	}
	if consume {
		delete(grants, key)
	}
	return stored, nil
}

func RevokeGrant(plain string) {
	if plain == "" {
		return
	}
	sum := sha256.Sum256([]byte(plain))
	key := hex.EncodeToString(sum[:])
	grantMu.Lock()
	delete(grants, key)
	grantMu.Unlock()
}

func RevokeLogin(loginSession string) {
	if loginSession == "" {
		return
	}
	loginSession = accounts.SessionLookupKey(loginSession)
	grantMu.Lock()
	for key, stored := range grants {
		if stored.loginSession == loginSession {
			delete(grants, key)
		}
	}
	grantMu.Unlock()
	if CloseLoginSessions != nil {
		CloseLoginSessions(loginSession)
	}
}

func RevokeUser(userUUID string) {
	if userUUID == "" {
		return
	}
	grantMu.Lock()
	for key, stored := range grants {
		if stored.userUUID == userUUID {
			delete(grants, key)
		}
	}
	grantMu.Unlock()
	if CloseUserSessions != nil {
		CloseUserSessions(userUUID)
	}
}

func RevokeAll() {
	grantMu.Lock()
	grants = make(map[string]storedGrant)
	grantMu.Unlock()
	if CloseAllSessions != nil {
		CloseAllSessions()
	}
}

func pruneGrantsLocked(now time.Time) {
	for key, stored := range grants {
		if !stored.expiresAt.After(now) {
			delete(grants, key)
		}
	}
}

func ResetForTest() {
	grantMu.Lock()
	grants = make(map[string]storedGrant)
	grantMu.Unlock()
	resetRateLimitsForTest()
}
