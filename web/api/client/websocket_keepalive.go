package client

import (
	"errors"
	"net"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/web/connection"
)

// websocketIdleTimeout is how long the server waits without any WebSocket
// frame (application payload or ping) before dropping the agent. Keepalive
// uses protocol ping/pong, not panel probe tasks (ICMP/TCP/HTTP) and not the
// report interval, so those can be disabled or set to 60s+.
const websocketIdleTimeout = 60 * time.Second

const websocketControlWriteTimeout = 5 * time.Second

func refreshAgentWebSocketReadDeadline(conn *connection.SafeConn) {
	refreshAgentWebSocketReadDeadlineFor(conn, websocketIdleTimeout)
}

func refreshAgentWebSocketReadDeadlineFor(conn *connection.SafeConn, idle time.Duration) {
	_ = conn.SetReadDeadline(time.Now().Add(idle))
}

func attachAgentWebSocketKeepalive(conn *connection.SafeConn) {
	attachAgentWebSocketKeepaliveFor(conn, websocketIdleTimeout)
}

func attachAgentWebSocketKeepaliveFor(conn *connection.SafeConn, idle time.Duration) {
	refreshAgentWebSocketReadDeadlineFor(conn, idle)
	conn.SetPingHandler(func(appData string) error {
		refreshAgentWebSocketReadDeadlineFor(conn, idle)
		err := conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(websocketControlWriteTimeout))
		if err == websocket.ErrCloseSent {
			return nil
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return err
		}
		if errors.As(err, &netErr) && netErr.Temporary() {
			return nil
		}
		return err
	})
}
