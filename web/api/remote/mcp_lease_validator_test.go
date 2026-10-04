package remote

import "testing"

func TestMCPTerminalLeaseValidatorUsesLeaseNotBrowserLogin(t *testing.T) {
	mcpLeaseValidatorMu.RLock()
	previous := mcpLeaseValidator
	mcpLeaseValidatorMu.RUnlock()
	t.Cleanup(func() { SetMCPTerminalLeaseValidator(previous) })

	live := true
	var calls int
	SetMCPTerminalLeaseValidator(func(leaseID, userUUID, nodeUUID string) bool {
		calls++
		return live && leaseID == "long-term-lease" && userUUID == "owner" && nodeUUID == "allowed-node"
	})
	for _, login := range []string{"", "expired-browser-session"} {
		session := &remoteSession{
			mcp: true, leaseID: "long-term-lease", UserUUID: "owner", UUID: "allowed-node", LoginSession: login,
		}
		if !mcpTerminalAuthorizationValid(session) {
			t.Fatalf("valid lease rejected with browser login %q", login)
		}
	}
	if calls != 2 {
		t.Fatalf("validator calls = %d, want 2", calls)
	}
	for _, session := range []*remoteSession{
		{mcp: true, leaseID: "other-lease", UserUUID: "owner", UUID: "allowed-node"},
		{mcp: true, leaseID: "long-term-lease", UserUUID: "other-owner", UUID: "allowed-node"},
		{mcp: true, leaseID: "long-term-lease", UserUUID: "owner", UUID: "out-of-scope-node"},
	} {
		if mcpTerminalAuthorizationValid(session) {
			t.Fatalf("terminal accepted mismatched lease/user/node binding: %+v", session)
		}
	}
	live = false
	if mcpTerminalAuthorizationValid(&remoteSession{mcp: true, leaseID: "long-term-lease", UserUUID: "owner", UUID: "allowed-node"}) {
		t.Fatal("terminal ignored lease validator revocation")
	}
}

func TestMCPTerminalLeaseValidatorFailsClosedWithoutHookOrLogin(t *testing.T) {
	mcpLeaseValidatorMu.RLock()
	previous := mcpLeaseValidator
	mcpLeaseValidatorMu.RUnlock()
	t.Cleanup(func() { SetMCPTerminalLeaseValidator(previous) })
	SetMCPTerminalLeaseValidator(nil)
	// Empty login fails before any account database access in the fallback.
	if mcpTerminalAuthorizationValid(&remoteSession{mcp: true, leaseID: "lease", UserUUID: "owner", UUID: "node"}) {
		t.Fatal("missing validator and browser login must fail closed")
	}
}
