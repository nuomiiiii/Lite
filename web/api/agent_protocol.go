package api

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	LiteRemoteSessionHeader = "X-Lite-Remote-Session"
	LiteRemoteTicketHeader  = "X-Lite-Remote-Ticket"
)

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

func AgentRemoteSessionHeader(c *gin.Context) string {
	return AgentProtocolHeader(c, LiteRemoteSessionHeader)
}

func AgentRemoteTicketHeader(c *gin.Context) string {
	return AgentProtocolHeader(c, LiteRemoteTicketHeader)
}
