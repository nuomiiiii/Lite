package remote

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRemoteHasRecentTrafficSkipsIdlePings(t *testing.T) {
	now := time.Now()
	if remoteHasRecentTraffic(time.Time{}, now) {
		t.Fatal("zero activity was treated as recent traffic")
	}
	if remoteHasRecentTraffic(now.Add(-remotePingInterval), now) {
		t.Fatal("activity at the ping interval was still treated as recent")
	}
	if !remoteHasRecentTraffic(now.Add(-time.Second), now) {
		t.Fatal("fresh terminal output should skip protocol pings")
	}
}

func TestBrowserHeartbeatDoesNotSkipAgentKeepalivePing(t *testing.T) {
	now := time.Now()
	session := &remoteSession{}
	session.recordHopTraffic(true, now)
	if !session.hopHasRecentTraffic(true, now) {
		t.Fatal("browser heartbeat should skip browser protocol pings")
	}
	if session.hopHasRecentTraffic(false, now) {
		t.Fatal("browser heartbeat skipped agent keepalive pings")
	}
	session.recordHopTraffic(false, now)
	if !session.hopHasRecentTraffic(false, now) {
		t.Fatal("agent traffic should skip agent protocol pings")
	}
}

func TestPingWriteErrorDoesNotEndABusyRemoteSession(t *testing.T) {
	for _, err := range []error{
		errors.New("websocket: write timeout"),
		&net.OpError{Op: "write", Err: errors.New("i/o timeout")},
		io.ErrClosedPipe,
		websocket.ErrCloseSent,
	} {
		if pingWriteErrorEndsSession(err) {
			t.Fatalf("ping write error %v ended the session", err)
		}
	}
	if pingWriteErrorEndsSession(nil) {
		t.Fatal("a successful ping ended the session")
	}
}
