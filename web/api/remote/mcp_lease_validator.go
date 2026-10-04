package remote

import "sync"

var mcpLeaseValidatorMu sync.RWMutex
var mcpLeaseValidator func(leaseID, userUUID, nodeUUID string) bool

// SetMCPTerminalLeaseValidator separates MCP authorization lifetime from a
// browser terminal's login lifetime. Only the MCP package registers this hook.
func SetMCPTerminalLeaseValidator(validator func(leaseID, userUUID, nodeUUID string) bool) {
	mcpLeaseValidatorMu.Lock()
	defer mcpLeaseValidatorMu.Unlock()
	mcpLeaseValidator = validator
}

func mcpTerminalAuthorizationValid(session *remoteSession) bool {
	mcpLeaseValidatorMu.RLock()
	validator := mcpLeaseValidator
	mcpLeaseValidatorMu.RUnlock()
	if validator != nil {
		return validator(session.leaseID, session.UserUUID, session.UUID)
	}
	return loginStillValid(session.UserUUID, session.LoginSession)
}
