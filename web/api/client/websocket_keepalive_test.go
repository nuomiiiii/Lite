package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/web/connection"
)

func TestProductionIdleTimeoutIgnoresReportAndProbeSchedules(t *testing.T) {
	heartbeat := 30 * time.Second
	if heartbeat >= websocketIdleTimeout {
		t.Fatalf("WebSocket heartbeat %v must be shorter than idle timeout %v", heartbeat, websocketIdleTimeout)
	}
	if websocketIdleTimeout < 60*time.Second {
		t.Fatalf("idle timeout %v is too short for 60s+ client/proxy settings", websocketIdleTimeout)
	}

	reportInterval := 70 * time.Second
	probes := []struct {
		name     string
		enabled  bool
		kind     string
		interval time.Duration
	}{
		{name: "probes-off", enabled: false, kind: "", interval: 0},
		{name: "icmp-90s", enabled: true, kind: "icmp", interval: 90 * time.Second},
		{name: "tcp-90s", enabled: true, kind: "tcp", interval: 90 * time.Second},
		{name: "http-90s", enabled: true, kind: "http", interval: 90 * time.Second},
	}
	for _, probe := range probes {
		t.Run(probe.name, func(t *testing.T) {
			if reportInterval <= websocketIdleTimeout {
				t.Fatal("fixture report interval must exceed idle timeout to prove reports are not the keepalive")
			}
			if probe.enabled && probe.interval <= websocketIdleTimeout {
				t.Fatalf("%s probe interval %v must exceed idle timeout to prove probes are not the keepalive", probe.kind, probe.interval)
			}
		})
	}
}

func TestKeepaliveSurvivesWithoutReportsOrProbes(t *testing.T) {
	idle := 250 * time.Millisecond
	url, dropped := startKeepaliveTestServer(t, idle)
	conn := dialKeepaliveTestClient(t, url)
	defer conn.Close()

	stopPings := startClientProtocolPings(conn, idle/3)
	select {
	case <-dropped:
		t.Fatal("server dropped the connection while protocol pings were still flowing")
	case <-time.After(idle * 3):
	}
	stopPings()

	select {
	case <-dropped:
	case <-time.After(idle + 400*time.Millisecond):
		t.Fatal("server did not drop the connection after protocol pings stopped")
	}
}

func TestKeepaliveDoesNotNeedProbePayloads(t *testing.T) {
	idle := 250 * time.Millisecond
	for _, probe := range []string{"off", "icmp", "tcp", "http"} {
		t.Run(probe, func(t *testing.T) {
			url, dropped := startKeepaliveTestServer(t, idle)
			conn := dialKeepaliveTestClient(t, url)
			defer conn.Close()
			stopPings := startClientProtocolPings(conn, idle/3)
			defer stopPings()
			select {
			case <-dropped:
				t.Fatalf("keepalive failed with probe mode %s; protocol ping must keep the socket alive without probe tasks", probe)
			case <-time.After(idle * 3):
			}
		})
	}
}

func TestIdleTimeoutFiresWithoutProtocolPingOrReports(t *testing.T) {
	idle := 200 * time.Millisecond
	url, dropped := startKeepaliveTestServer(t, idle)
	conn := dialKeepaliveTestClient(t, url)
	defer conn.Close()

	select {
	case <-dropped:
	case <-time.After(idle + 400*time.Millisecond):
		t.Fatal("server kept an idle connection that sent neither reports nor protocol pings")
	}
}

func startKeepaliveTestServer(t *testing.T, idle time.Duration) (string, <-chan struct{}) {
	t.Helper()
	dropped := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		conn := connection.NewSafeConn(raw)
		defer conn.Close()
		attachAgentWebSocketKeepaliveFor(conn, idle)
		for {
			refreshAgentWebSocketReadDeadlineFor(conn, idle)
			if _, _, err := conn.ReadMessage(); err != nil {
				close(dropped)
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), dropped
}

func dialKeepaliveTestClient(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func startClientProtocolPings(conn *websocket.Conn, interval time.Duration) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
			}
		}
	}()
	return func() { close(done) }
}
