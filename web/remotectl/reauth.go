package remotectl

import (
	"errors"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/accounts"
)

const (
	reauthWindow      = 3 * time.Minute
	reauthMaxFailures = 5
	rateLimitCapacity = 4096
)

var (
	ErrPasswordRequired = errors.New("administrator password is required")
	ErrPasswordInvalid  = errors.New("administrator password is incorrect")
	ErrOTPRequired      = errors.New("2FA code is required")
	ErrOTPInvalid       = errors.New("Invalid 2FA code")
	ErrSSOReauth        = errors.New("SSO accounts cannot use remote management until they re-authenticate")
	ErrRateLimited      = errors.New("too many failed remote authorization attempts")
)

type rateBucket struct {
	windowStart time.Time
	failures    int
}

var (
	rateMu     sync.Mutex
	rateByUser = make(map[string]rateBucket)
	rateByIP   = make(map[string]rateBucket)
	rateByPair = make(map[string]rateBucket)
)

func Reauthorize(userUUID, password, otp, clientIP string) error {
	if userUUID == "" {
		return ErrGrantPrincipal
	}
	if throttled(userUUID, clientIP) {
		return ErrRateLimited
	}
	user, err := accounts.GetUserByUUID(userUUID)
	if err != nil {
		recordFailure(userUUID, clientIP)
		return ErrGrantPrincipal
	}
	if user.TwoFactor != "" {
		if otp == "" {
			recordFailure(userUUID, clientIP)
			return ErrOTPRequired
		}
		valid, verifyErr := accounts.Verify2Fa(userUUID, otp)
		if verifyErr != nil || !valid {
			recordFailure(userUUID, clientIP)
			return ErrOTPInvalid
		}
		clearPairFailures(userUUID, clientIP)
		return nil
	}
	if user.Passwd == "" && (user.SSOType != "" || user.SSOID != "") {
		recordFailure(userUUID, clientIP)
		return ErrSSOReauth
	}
	if password == "" {
		recordFailure(userUUID, clientIP)
		return ErrPasswordRequired
	}
	if err := accounts.VerifyPasswordForUUID(userUUID, password); err != nil {
		if accounts.IsPasswordBusy(err) {
			return err
		}
		recordFailure(userUUID, clientIP)
		return ErrPasswordInvalid
	}
	clearPairFailures(userUUID, clientIP)
	return nil
}

func throttled(userUUID, clientIP string) bool {
	rateMu.Lock()
	defer rateMu.Unlock()
	now := time.Now()
	pruneRateLimitsLocked(now)
	return bucketThrottled(rateByUser[userUUID], now) ||
		bucketThrottled(rateByIP[clientIP], now) ||
		bucketThrottled(rateByPair[rateKey(userUUID, clientIP)], now)
}

func bucketThrottled(bucket rateBucket, now time.Time) bool {
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) > reauthWindow {
		return false
	}
	return bucket.failures >= reauthMaxFailures
}

func recordFailure(userUUID, clientIP string) {
	rateMu.Lock()
	defer rateMu.Unlock()
	now := time.Now()
	pruneRateLimitsLocked(now)
	rateByUser[userUUID] = bumpBucket(rateByUser[userUUID], now)
	rateByIP[clientIP] = bumpBucket(rateByIP[clientIP], now)
	rateByPair[rateKey(userUUID, clientIP)] = bumpBucket(rateByPair[rateKey(userUUID, clientIP)], now)
	evictRateLimitsLocked()
}

func bumpBucket(bucket rateBucket, now time.Time) rateBucket {
	if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) > reauthWindow {
		return rateBucket{windowStart: now, failures: 1}
	}
	bucket.failures++
	return bucket
}

func clearPairFailures(userUUID, clientIP string) {
	rateMu.Lock()
	delete(rateByPair, rateKey(userUUID, clientIP))
	rateMu.Unlock()
}

func rateKey(userUUID, clientIP string) string {
	return userUUID + "|" + clientIP
}

func pruneRateLimitsLocked(now time.Time) {
	pruneBucketMapLocked(rateByUser, now)
	pruneBucketMapLocked(rateByIP, now)
	pruneBucketMapLocked(rateByPair, now)
}

func pruneBucketMapLocked(buckets map[string]rateBucket, now time.Time) {
	for key, bucket := range buckets {
		if bucket.windowStart.IsZero() || now.Sub(bucket.windowStart) > reauthWindow {
			delete(buckets, key)
		}
	}
}

func evictRateLimitsLocked() {
	evictOldestLocked(rateByUser)
	evictOldestLocked(rateByIP)
	evictOldestLocked(rateByPair)
}

func evictOldestLocked(buckets map[string]rateBucket) {
	for len(buckets) > rateLimitCapacity {
		var oldestKey string
		var oldest time.Time
		for key, bucket := range buckets {
			if oldestKey == "" || bucket.windowStart.Before(oldest) {
				oldestKey = key
				oldest = bucket.windowStart
			}
		}
		delete(buckets, oldestKey)
	}
}

func resetRateLimitsForTest() {
	rateMu.Lock()
	rateByUser = make(map[string]rateBucket)
	rateByIP = make(map[string]rateBucket)
	rateByPair = make(map[string]rateBucket)
	rateMu.Unlock()
}

func IsRateLimited(err error) bool {
	return errors.Is(err, ErrRateLimited)
}
