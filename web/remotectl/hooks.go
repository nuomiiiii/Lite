package remotectl

// CloseLoginSessions and CloseUserSessions are registered by the remote
// session package so login/account changes can close sockets without an import cycle.
var (
	CloseLoginSessions func(loginSession string)
	CloseUserSessions  func(userUUID string)
	CloseAllSessions   func()
)
