package remotectl

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestConsumeGrantRejectsEmpty(t *testing.T) {
	ResetForTest()
	if err := ConsumeGrant("", "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantRequired) {
		t.Fatalf("empty grant error = %v", err)
	}
}

func TestIssueAndLookupGrant(t *testing.T) {
	ResetForTest()
	plain, expires, err := IssueGrant("user-a", "login-a", ScopeRemote, "page-a")
	if err != nil || plain == "" || !expires.After(time.Now()) {
		t.Fatalf("IssueGrant() = %q %v %v", plain, expires, err)
	}
	if err := ConsumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
	if err := ConsumeGrant(plain, "user-b", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantPrincipal) {
		t.Fatalf("cross-user grant error = %v", err)
	}
	if err := ConsumeGrant(plain, "user-a", "login-b", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantPrincipal) {
		t.Fatalf("cross-login grant error = %v", err)
	}
	if err := ConsumeGrant(plain, "user-a", "login-a", ScopeExec, "page-a"); !errors.Is(err, ErrGrantScope) {
		t.Fatalf("cross-scope grant error = %v", err)
	}
	if err := ConsumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-b"); !errors.Is(err, ErrGrantWorkspace) {
		t.Fatalf("cross-page grant error = %v", err)
	}
}

func TestIssueRemoteGrantRequiresPage(t *testing.T) {
	ResetForTest()
	if _, _, err := IssueGrant("user-a", "login-a", ScopeRemote, ""); !errors.Is(err, ErrGrantWorkspace) {
		t.Fatalf("empty page error = %v", err)
	}
}

func TestRevokeGrantAndLogin(t *testing.T) {
	ResetForTest()
	plain, _, err := IssueGrant("user-a", "login-a", ScopeRemote, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	RevokeGrant(plain)
	if err := ConsumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("revoked grant error = %v", err)
	}
	plain, _, err = IssueGrant("user-a", "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	RevokeLogin("login-a")
	if err := ConsumeGrant(plain, "user-a", "login-a", ScopeExec, "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("login-revoked grant error = %v", err)
	}
}

func TestGrantExpiry(t *testing.T) {
	ResetForTest()
	plain, _, err := IssueGrant("user-a", "login-a", ScopeRemote, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	grantMu.Lock()
	for key, stored := range grants {
		stored.expiresAt = time.Now().Add(-time.Second)
		grants[key] = stored
	}
	grantMu.Unlock()
	if err := ConsumeGrant(plain, "user-a", "login-a", ScopeRemote, "page-a"); !errors.Is(err, ErrGrantExpired) && !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("expired grant error = %v", err)
	}
}

func TestTakeExecGrantIsSingleUse(t *testing.T) {
	ResetForTest()
	plain, expires, err := IssueGrant("user-a", "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	gotExpires, err := TakeExecGrant(plain, "user-a", "login-a", "page-a")
	if err != nil {
		t.Fatal(err)
	}
	if !gotExpires.Equal(expires) {
		t.Fatalf("expires = %v, want %v", gotExpires, expires)
	}
	if _, err := TakeExecGrant(plain, "user-a", "login-a", "page-a"); !errors.Is(err, ErrGrantInvalid) {
		t.Fatalf("reused exec grant error = %v", err)
	}
}

func TestConcurrentTakeExecGrant(t *testing.T) {
	ResetForTest()
	plain, _, err := IssueGrant("user-a", "login-a", ScopeExec, "page-a")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 8)
	wg.Add(len(results))
	for i := range results {
		go func(i int) {
			defer wg.Done()
			_, results[i] = TakeExecGrant(plain, "user-a", "login-a", "page-a")
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("concurrent consume successes = %d, want 1", ok)
	}
}

func TestRotateExecGrantKeepsAbsoluteExpiry(t *testing.T) {
	ResetForTest()
	expires := time.Now().Add(3 * time.Minute)
	next, gotExpires, err := RotateExecGrant("user-a", "login-a", "page-a", expires)
	if err != nil || next == "" {
		t.Fatal(err)
	}
	if !gotExpires.Equal(expires) {
		t.Fatalf("rotated expiry = %v, want %v", gotExpires, expires)
	}
	if _, err := TakeExecGrant(next, "user-a", "login-a", "page-a"); err != nil {
		t.Fatal(err)
	}
}

func TestReauthRateLimit(t *testing.T) {
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("missing-user", "127.0.0.1")
	}
	if err := Reauthorize("missing-user", "x", "", "127.0.0.1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit error = %v", err)
	}
}

func TestReauthRateLimitSplitsUserAndIP(t *testing.T) {
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("user-a", "10.0.0.1")
	}
	if err := Reauthorize("user-b", "x", "", "10.0.0.1"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("IP bucket should block other users: %v", err)
	}
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("user-a", "10.0.0.8")
	}
	if err := Reauthorize("user-a", "x", "", "10.0.0.9"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("user bucket should block other IPs: %v", err)
	}
	clearPairFailures("user-a", "10.0.0.8")
	if err := Reauthorize("user-a", "x", "", "10.0.0.9"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("clearing one pair must not reset the user bucket: %v", err)
	}
}

func TestConcurrentReauthFailuresShareBuckets(t *testing.T) {
	ResetForTest()
	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 20; i++ {
		go func() {
			defer wg.Done()
			recordFailure("user-a", "10.0.0.4")
		}()
	}
	wg.Wait()
	if !throttled("user-a", "10.0.0.4") {
		t.Fatal("concurrent failures did not trip the shared buckets")
	}
}

func TestReauthWindowExpiryAndCapacity(t *testing.T) {
	ResetForTest()
	for i := 0; i < reauthMaxFailures; i++ {
		recordFailure("user-a", "10.0.0.5")
	}
	expired := time.Now().Add(-reauthWindow - time.Second)
	rateMu.Lock()
	for _, buckets := range []map[string]rateBucket{rateByUser, rateByIP, rateByPair} {
		for key, bucket := range buckets {
			bucket.windowStart = expired
			buckets[key] = bucket
		}
	}
	rateMu.Unlock()
	if throttled("user-a", "10.0.0.5") {
		t.Fatal("expired rate-limit window still blocked")
	}

	ResetForTest()
	for i := 0; i < rateLimitCapacity+32; i++ {
		recordFailure("user-"+strconv.Itoa(i), "10.0.0.6")
	}
	rateMu.Lock()
	defer rateMu.Unlock()
	if len(rateByUser) > rateLimitCapacity || len(rateByIP) > rateLimitCapacity || len(rateByPair) > rateLimitCapacity {
		t.Fatalf("rate limit maps grew past cap user=%d ip=%d pair=%d", len(rateByUser), len(rateByIP), len(rateByPair))
	}
}
