package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func TestRemoteBrowserOriginSupportsInstalledMobileWebApps(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "same origin", origin: "https://monitor.example", want: true},
		{name: "opaque mobile web app origin", origin: "null", want: true},
		{name: "missing mobile web app origin", origin: "", want: true},
		{name: "cross origin", origin: "https://attacker.example", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "https://monitor.example/api/admin/client/remote", nil)
			request.Host = "monitor.example"
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
