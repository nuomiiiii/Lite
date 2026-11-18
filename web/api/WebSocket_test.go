package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	"github.com/nuomiiiii/lite/utils/instancekey"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	cleanup := instancekey.SetupTempFileForTest()
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:ws_api_pkg_test?mode=memory&cache=shared"
	db := dbcore.GetDBInstance()
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func TestRemoteBrowserOriginRejectsEmptyNullAndCrossOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "same origin", origin: "https://monitor.example", want: true},
		{name: "opaque origin", origin: "null", want: false},
		{name: "missing origin", origin: "", want: true},
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

func TestPublicClientsSnapshotHidesNodeImmediately(t *testing.T) {
	db := dbcore.GetDBInstance()
	var sqlQueries atomic.Int64
	const cbName = "test:ws_snapshot_query_counter"
	_ = db.Callback().Query().After("gorm:query").Register(cbName, func(*gorm.DB) {
		sqlQueries.Add(1)
	})
	t.Cleanup(func() { _ = db.Callback().Query().Remove(cbName) })

	const nodeUUID = "ws-hide-node-1"
	if err := db.Create(&models.Client{
		UUID:   nodeUUID,
		Token:  "ws-hide-tok",
		Name:   "PublicThenHidden",
		Hidden: false,
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Where("uuid = ?", nodeUUID).Delete(&models.Client{}).Error })

	agent_runtime.SetPresence(nodeUUID, 1, true)
	agent_runtime.RecordReport(v2.Report{
		UUID:      nodeUUID,
		CPU:       v2.CPUReport{Usage: 22.5},
		UpdatedAt: time.Now().UTC(),
	})
	t.Cleanup(func() { agent_runtime.DeleteConnectedClients(nodeUUID) })

	// 1. Guest snapshot initially includes the visible online node (and populates wsSnapshotCache).
	beforeRaw, err := getCachedWSClientsSnapshot(context.Background(), db, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(beforeRaw), nodeUUID) {
		t.Fatalf("expected visible node %s in initial guest snapshot: %s", nodeUUID, beforeRaw)
	}

	// 2. Repeated guest snapshot pulls within the 500ms TTL MUST hit memory cache with ZERO SQLite queries!
	sqlQueries.Store(0)
	for range 5 {
		hitRaw, err := getCachedWSClientsSnapshot(context.Background(), db, false, "")
		if err != nil {
			t.Fatal(err)
		}
		if string(hitRaw) != string(beforeRaw) {
			t.Fatalf("cached snapshot mismatch: %s", hitRaw)
		}
	}
	if got := sqlQueries.Load(); got != 0 {
		t.Fatalf("expected 0 SQLite queries on cached guest WS snapshot hits, got %d", got)
	}

	// 3. Admin toggles hidden = true via SaveClientForDispatch -> CacheGeneration bumps and guest snapshot hides at 0ms!
	if _, err := clients.SaveClientForDispatch(map[string]any{"uuid": nodeUUID, "hidden": true}); err != nil {
		t.Fatal(err)
	}
	afterRaw, err := getCachedWSClientsSnapshot(context.Background(), db, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(afterRaw), nodeUUID) {
		t.Fatalf("hidden node %s leaked in guest WS snapshot after hiding: %s", nodeUUID, afterRaw)
	}

	// Single-node guest query ("get <uuid>") must also immediately omit it.
	single := buildWSClientsPayload(context.Background(), db, false, "", nodeUUID)
	if len(single.Online) != 0 || len(single.Data) != 0 {
		raw, _ := json.Marshal(single)
		t.Fatalf("hidden node leaked in single-node guest payload: %s", raw)
	}

	// Logged-in admin still sees the hidden node.
	adminRaw, err := getCachedWSClientsSnapshot(context.Background(), db, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adminRaw), nodeUUID) {
		t.Fatalf("expected admin WS snapshot to retain hidden node %s: %s", nodeUUID, adminRaw)
	}
}
