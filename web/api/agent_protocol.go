package api

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	LiteTerminalSessionHeader   = "X-Lite-Terminal-Session"
	KomariTerminalSessionHeader = "X-Komari-Terminal-Session"
	LiteRemoteSessionHeader     = "X-Lite-Remote-Session"
	KomariRemoteSessionHeader   = "X-Komari-Remote-Session"
	LiteRemoteTicketHeader      = "X-Lite-Remote-Ticket"
	KomariRemoteTicketHeader    = "X-Komari-Remote-Ticket"
)

// AgentProtocolHeader reads Lite headers first, then Komari headers.
// Lite-agent 2.3.0.0 speaks Lite natively; Komari headers stay for older agents.
func AgentProtocolHeader(c *gin.Context, names ...string) string {
	if c == nil || c.Request == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
			return value
		}
	}
	return ""
}

func AgentTerminalSessionHeader(c *gin.Context) string {
	return AgentProtocolHeader(c, LiteTerminalSessionHeader, KomariTerminalSessionHeader)
}

func AgentRemoteSessionHeader(c *gin.Context) string {
	return AgentProtocolHeader(c, LiteRemoteSessionHeader, KomariRemoteSessionHeader)
}

func AgentRemoteTicketHeader(c *gin.Context) string {
	return AgentProtocolHeader(c, LiteRemoteTicketHeader, KomariRemoteTicketHeader)
}
