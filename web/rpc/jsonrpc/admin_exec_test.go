package jsonrpc

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/tasks"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent "github.com/nuomiiiii/lite/web/agent"
)

var errPersistTerminal = errors.New("persist failed")

func TestUniqueUUIDsDropsDuplicatesAndEmpty(t *testing.T) {
	got := uniqueUUIDs([]string{"a", "", "b", "a", "c", "b"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("uniqueUUIDs = %#v", got)
	}
}

func TestClassifyRemoteExecTargetsKeepsExistingSemantics(t *testing.T) {
	known := map[string]models.Client{
		"live": {
			UUID:                 "live",
			RemoteProtocol:       2,
			RemoteControlEnabled: true,
		},
		"queued": {
			UUID:                 "queued",
			RemoteProtocol:       2,
			RemoteControlEnabled: true,
		},
		"offline": {
			UUID:                 "offline",
			RemoteProtocol:       2,
			RemoteControlEnabled: true,
		},
		"disabled": {
			UUID:                 "disabled",
			RemoteProtocol:       2,
			RemoteControlEnabled: false,
		},
		"host": {
			UUID:                   "host",
			RemoteProtocol:         2,
			RemoteControlEnabled:   true,
			RemoteControlProtected: true,
		},
		"legacy": {
			UUID:                 "legacy",
			RemoteProtocol:       1,
			RemoteControlEnabled: true,
		},
	}
	connected := map[string]bool{"live": true, "host": true}
	online := map[string]bool{"live": true, "queued": true, "host": true}
	classified := classifyRemoteExecTargets(
		uniqueUUIDs([]string{"live", "queued", "offline", "missing", "disabled", "host", "legacy", "live"}),
		known,
		func(uuid string) bool { return connected[uuid] },
		func(uuid string) bool { return online[uuid] },
	)
	if strings.Join(classified.live, ",") != "live,host" {
		t.Fatalf("live = %#v", classified.live)
	}
	if strings.Join(classified.queued, ",") != "queued" {
		t.Fatalf("queued = %#v", classified.queued)
	}
	if strings.Join(classified.offline, ",") != "offline" {
		t.Fatalf("offline = %#v", classified.offline)
	}
	if strings.Join(classified.unavailable, ",") != "missing,disabled,legacy" {
		t.Fatalf("unavailable = %#v", classified.unavailable)
	}
}

func TestAdminExecDoesNotCopyConnectedClientMap(t *testing.T) {
	src, err := os.ReadFile("admin.system.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := extractGoFunc(string(src), "func adminExec")
	if fn == "" {
		t.Fatal("adminExec not found")
	}
	if strings.Contains(fn, "GetConnectedClients()") {
		t.Fatal("adminExec still copies the full connected client map")
	}
	if !strings.Contains(fn, "DispatchV2ExecEvent(") {
		t.Fatal("adminExec does not enqueue exec events before notify")
	}
	if !strings.Contains(fn, "GuardRemoteDelivery(") {
		t.Fatal("adminExec does not hold the remote delivery gate while dispatching")
	}
	if strings.Contains(fn, "conn.WriteJSON") {
		t.Fatal("adminExec still writes exec tasks without an event ID")
	}
	if strings.Contains(fn, "GetClientByUUID(") {
		t.Fatal("adminExec still looks up clients one UUID at a time")
	}
	if strings.Contains(fn, "_ = tasks.SaveTaskResults") {
		t.Fatal("adminExec still ignores SaveTaskResults errors")
	}
	if !strings.Contains(fn, "persistExecTerminalResults(") {
		t.Fatal("adminExec does not persist terminal results through persistExecTerminalResults")
	}
}

func TestExpiredExecHandlerDoesNotSwallowPersistErrors(t *testing.T) {
	src, err := os.ReadFile("admin.system.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if strings.Contains(text, "_ = tasks.SaveIncomingTaskResult") {
		t.Fatal("expired exec handler still swallows SaveIncomingTaskResult errors")
	}
	if !strings.Contains(text, "persistIncomingTaskResult(") {
		t.Fatal("expired exec handler does not persist delivery timeouts")
	}
}

func TestAdminEditSettingsCancelsQueuedExecWhenRemoteTurnsOff(t *testing.T) {
	src, err := os.ReadFile("admin.misc.go")
	if err != nil {
		t.Fatal(err)
	}
	fn := extractGoFunc(string(src), "func adminEditSettings")
	if fn == "" {
		t.Fatal("adminEditSettings not found")
	}
	if !strings.Contains(fn, "MethodAgentRemote") || !strings.Contains(fn, "MethodAgentExec") {
		t.Fatal("disabling remote management does not clear queued agent.remote.request and agent.exec")
	}
	if !strings.Contains(fn, "DrainRemoteDelivery(") {
		t.Fatal("disabling remote management does not hold the remote delivery gate")
	}
	if !strings.Contains(fn, "cancelUndeliveredRemoteExec(") {
		t.Fatal("disabling remote management does not persist cancelled exec results")
	}
	if !strings.Contains(fn, "未能写入已取消任务结果") {
		t.Fatal("disabling remote management still swallows cancel persist errors")
	}
}

func TestPersistExecTerminalResultsDoesNotIgnoreErrors(t *testing.T) {
	calls := 0
	persistTaskResults = func(taskId string, clientIDs []string, result string, exitCode int, timestamp time.Time) error {
		calls++
		if calls == 2 {
			return errPersistTerminal
		}
		return nil
	}
	t.Cleanup(func() { persistTaskResults = tasks.SaveTaskResults })
	err := persistExecTerminalResults("task-persist", []string{"off"}, []string{"fail"}, []string{"unavail"}, time.Now().UTC())
	if err == nil {
		t.Fatal("persistExecTerminalResults ignored a SaveTaskResults error")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestCancelUndeliveredRemoteExecReturnsPersistError(t *testing.T) {
	previous := cancelUndeliveredTaskResult
	t.Cleanup(func() { cancelUndeliveredTaskResult = previous })
	cancelUndeliveredTaskResult = func(taskId, clientId, result string) error {
		return errPersistTerminal
	}
	err := cancelUndeliveredRemoteExec([]agent.RemovedV2Event{{
		UUID: "node-a",
		Event: v2.Event{
			Method: v2.MethodAgentExec,
			Params: v2.ExecParams{TaskID: "task-cancel", Command: "true"},
		},
	}})
	if err == nil {
		t.Fatal("cancel persist error was swallowed")
	}
}

func TestCancelExecTaskClientsReturnsPersistError(t *testing.T) {
	previous := cancelUndeliveredTaskResult
	t.Cleanup(func() { cancelUndeliveredTaskResult = previous })
	cancelUndeliveredTaskResult = func(taskId, clientId, result string) error {
		return errPersistTerminal
	}
	if err := cancelExecTaskClients("task-gate", []string{"node-a"}); err == nil {
		t.Fatal("gate-fail cancel persist error was swallowed")
	}
}

func extractGoFunc(src, signature string) string {
	start := strings.Index(src, signature)
	if start < 0 {
		return ""
	}
	rest := src[start+len(signature):]
	next := strings.Index(rest, "\nfunc ")
	if next < 0 {
		return src[start:]
	}
	return src[start : start+len(signature)+next]
}
