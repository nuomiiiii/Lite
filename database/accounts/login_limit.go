package accounts

import (
	"sync"
	"time"
)

const (
	loginLimitWindow        = 3 * time.Minute
	loginLimitMaxFailures   = 5
	loginLimitCapacity      = 4096
	loginLimitPruneEveryOps = 256
	loginLimitPruneMinAge   = time.Minute
)

type loginLimitBucket struct {
	windowStart time.Time
	failures    int
}

var (
	loginLimitMu        sync.Mutex
	loginLimitByPair    = make(map[string]loginLimitBucket)
	loginLimitOps       int
	loginLimitLastPrune time.Time
)

func loginThrottled(clientIP, username string) bool {
	if clientIP == "" || username == "" {
		return false
	}
	loginLimitMu.Lock()
	defer loginLimitMu.Unlock()
	now := time.Now()
	loginLimitOps++
	maybePruneLoginLimitsLocked(now, false)
	key := loginLimitKey(clientIP, username)
	bucket, ok := loginLimitByPair[key]
	if !ok {
		return false
	}
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) > loginLimitWindow {
		delete(loginLimitByPair, key)
		return false
	}
	return bucket.failures >= loginLimitMaxFailures
}

func recordLoginFailure(clientIP, username string) {
	if clientIP == "" || username == "" {
		return
	}
	loginLimitMu.Lock()
	defer loginLimitMu.Unlock()
	now := time.Now()
	loginLimitOps++
	key := loginLimitKey(clientIP, username)
	loginLimitByPair[key] = bumpLoginBucket(loginLimitByPair[key], now)
	if len(loginLimitByPair) > loginLimitCapacity {
		pruneLoginLimitsLocked(now)
		evictLoginLimitsLocked()
		loginLimitLastPrune = now
		loginLimitOps = 0
		return
	}
	maybePruneLoginLimitsLocked(now, false)
}

func clearLoginFailures(clientIP, username string) {
	if clientIP == "" || username == "" {
		return
	}
	loginLimitMu.Lock()
	delete(loginLimitByPair, loginLimitKey(clientIP, username))
	loginLimitMu.Unlock()
}

func RecordLoginFailure(clientIP, username string) {
	recordLoginFailure(clientIP, username)
}

func ClearLoginFailures(clientIP, username string) {
	clearLoginFailures(clientIP, username)
}

func loginLimitKey(clientIP, username string) string {
	return clientIP + "\x1f" + username
}

func bumpLoginBucket(bucket loginLimitBucket, now time.Time) loginLimitBucket {
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) > loginLimitWindow {
		return loginLimitBucket{windowStart: now, failures: 1}
	}
	bucket.failures++
	return bucket
}

func maybePruneLoginLimitsLocked(now time.Time, force bool) {
	if !force {
		if loginLimitLastPrune.IsZero() {
			loginLimitLastPrune = now
			if loginLimitOps < loginLimitPruneEveryOps {
				return
			}
		} else if loginLimitOps < loginLimitPruneEveryOps && now.Sub(loginLimitLastPrune) < loginLimitPruneMinAge {
			return
		}
	}
	pruneLoginLimitsLocked(now)
	loginLimitLastPrune = now
	loginLimitOps = 0
}

func pruneLoginLimitsLocked(now time.Time) {
	for key, bucket := range loginLimitByPair {
		if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) > loginLimitWindow {
			delete(loginLimitByPair, key)
		}
	}
}

func evictLoginLimitsLocked() {
	for len(loginLimitByPair) > loginLimitCapacity {
		var oldestKey string
		var oldest time.Time
		for key, bucket := range loginLimitByPair {
			if oldestKey == "" || bucket.windowStart.Before(oldest) {
				oldestKey = key
				oldest = bucket.windowStart
			}
		}
		delete(loginLimitByPair, oldestKey)
	}
}

func ResetLoginLimitsForTest() {
	loginLimitMu.Lock()
	loginLimitByPair = make(map[string]loginLimitBucket)
	loginLimitOps = 0
	loginLimitLastPrune = time.Time{}
	loginLimitMu.Unlock()
}

func LoginLimitSizeForTest() int {
	loginLimitMu.Lock()
	defer loginLimitMu.Unlock()
	return len(loginLimitByPair)
}

func ExpireLoginLimitsForTest(age time.Duration) {
	loginLimitMu.Lock()
	defer loginLimitMu.Unlock()
	cutoff := time.Now().Add(-age)
	for key, bucket := range loginLimitByPair {
		bucket.windowStart = cutoff
		loginLimitByPair[key] = bucket
	}
}
