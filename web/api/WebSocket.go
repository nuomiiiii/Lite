package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/web/security"
)

type WebSocketUpgradeOption func(*websocket.Upgrader)

func IsWebSocketUpgrade(c *gin.Context) bool {
	return websocket.IsWebSocketUpgrade(c.Request)
}

func EnableWebSocketCompression(upgrader *websocket.Upgrader) {
	upgrader.EnableCompression = true
}

// RequireRemoteBrowserOrigin keeps normal same-origin enforcement while
// accepting opaque origins used by some installed mobile web apps. The remote
// endpoint additionally requires an administrator cookie and a single-use,
// high-entropy browser ticket before it can attach to a session.
func RequireRemoteBrowserOrigin(upgrader *websocket.Upgrader) {
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return security.RemoteOriginAllowed(r)
	}
}

func AllowAgentWebSocket(upgrader *websocket.Upgrader) {
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return r.Header.Get("Origin") == ""
	}
}

func UpgradeWebSocket(c *gin.Context, options ...WebSocketUpgradeOption) (*websocket.Conn, error) {
	if !IsWebSocketUpgrade(c) {
		return nil, fmt.Errorf("require websocket upgrade")
	}
	upgrader := websocket.Upgrader{
		CheckOrigin: CheckWebSocketOrigin,
	}
	for _, option := range options {
		option(&upgrader)
	}
	return upgrader.Upgrade(c.Writer, c.Request, nil)
}

func CheckWebSocketOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if strings.EqualFold(os.Getenv("LITE_WS_DISABLE_ORIGIN"), "true") {
		return true
	}
	if security.IsAPIKeyRequest(r) {
		return true
	}
	if origin == "" && r.URL.Query().Get("token") != "" {
		return true
	}
	enabled, _ := config.GetAs[bool](config.WsOriginCheckEnabledKey, true)
	if !enabled {
		return true
	}
	if origin == "" {
		return false
	}
	if security.OriginMatchesRequest(origin, r) {
		return true
	}
	allowlist, _ := config.GetAs[string](config.WsAllowedOriginsKey, "")
	return security.OriginInAllowlist(origin, allowlist)
}
