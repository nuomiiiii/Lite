package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func TestRemoteBrowserOriginRejectsEmptyNullAndCrossOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "same origin", origin: "https://monitor.example", want: true},
		{name: "opaque origin", origin: "null", want: false},
		{name: "missing origin", origin: "", want: false},
		{name: "cross origin", origin: "https://attacker.example", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "https://monitor.example/api/admin/client/remote", nil)
			request.Host = "monitor.example"
			request.RemoteAddr = "203.0.113.10:443"
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			upgrader := &websocket.Upgrader{}
			RequireRemoteBrowserOrigin(upgrader)
			if got := upgrader.CheckOrigin(request); got != test.want {
				t.Fatalf("origin %q accepted=%v, want %v", test.origin, got, test.want)
			}
		})
	}
}

func TestRemoteBrowserOriginAllowsLoopbackVite(t *testing.T) {
	request := httptest.NewRequest("GET", "http://127.0.0.1:27777/api/admin/client/remote", nil)
	request.Host = "127.0.0.1:27777"
	request.RemoteAddr = "127.0.0.1:5273"
	request.Header.Set("Origin", "http://127.0.0.1:5273")
	upgrader := &websocket.Upgrader{}
	RequireRemoteBrowserOrigin(upgrader)
	if !upgrader.CheckOrigin(request) {
		t.Fatal("loopback Vite origin was rejected")
	}
}
