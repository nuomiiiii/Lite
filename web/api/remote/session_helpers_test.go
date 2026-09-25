package remote

import (
	"time"
)

func pruneStaleSessions(now time.Time) {
	sessionsMu.Lock()
	pruned := takeStaleSessionsLocked(now)
	sessionsMu.Unlock()
	closeTakenSessions(pruned)
}
