package tasks

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

var errInjectedTaskResultBatch = errors.New("injected remainder failure")

func TestClearTaskResultsByTimeBeforeUsesUTCTimeValue(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_cleanup_time?mode=memory&cache=shared"
	db := dbcore.GetDBInstance()

	taskID := "task-cleanup-time"
	if err := db.Create(&models.Task{TaskId: taskID, Command: "true"}).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	cutoff := time.Date(2026, 7, 17, 12, 0, 0, 123456789, time.UTC)
	results := []models.TaskResult{
		{TaskId: taskID, Client: "old", CreatedAt: cutoff.Add(-time.Nanosecond)},
		{TaskId: taskID, Client: "boundary", CreatedAt: cutoff},
		{TaskId: taskID, Client: "new", CreatedAt: cutoff.Add(time.Nanosecond)},
	}
	if err := db.Create(&results).Error; err != nil {
		t.Fatalf("create task results: %v", err)
	}

	localCutoff := cutoff.In(time.FixedZone("UTC+8", 8*60*60))
	if err := ClearTaskResultsByTimeBefore(localCutoff); err != nil {
		t.Fatalf("clear task results: %v", err)
	}
	var remaining []models.TaskResult
	if err := db.Where("task_id = ?", taskID).Order("created_at").Find(&remaining).Error; err != nil {
		t.Fatalf("load remaining results: %v", err)
	}
	if len(remaining) != 2 || remaining[0].Client != "boundary" || remaining[1].Client != "new" {
		t.Fatalf("remaining results = %#v, want boundary and new", remaining)
	}
}

func TestCreateTaskChunksAndSaveTaskResultsBySet(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_cleanup_time?mode=memory&cache=shared"
	_ = dbcore.GetDBInstance()

	clients := make([]string, 401)
	for i := range clients {
		clients[i] = "node-" + itoa(i)
	}
	taskID := "task-chunk-401"
	if err := CreateTask(taskID, clients, "uname"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	results, err := GetTaskResultsByTaskId(taskID)
	if err != nil {
		t.Fatalf("GetTaskResultsByTaskId: %v", err)
	}
	if len(results) != 401 {
		t.Fatalf("task results = %d, want 401", len(results))
	}

	offline := clients[:3]
	failed := clients[3:5]
	unavailable := clients[5:6]
	now := time.Now().UTC()
	if err := SaveTaskResults(taskID, offline, "Client offline!", -1, now); err != nil {
		t.Fatalf("SaveTaskResults offline: %v", err)
	}
	if err := SaveTaskResults(taskID, failed, "delivery failed", -1, now); err != nil {
		t.Fatalf("SaveTaskResults delivery: %v", err)
	}
	if err := SaveTaskResults(taskID, unavailable, "remote control unavailable", -1, now); err != nil {
		t.Fatalf("SaveTaskResults unavailable: %v", err)
	}
	results, err = GetTaskResultsByTaskId(taskID)
	if err != nil {
		t.Fatalf("reload results: %v", err)
	}
	got := map[string]string{}
	for _, result := range results {
		if result.Result != "" {
			got[result.Client] = result.Result
		}
	}
	if got[offline[0]] != "Client offline!" || got[failed[0]] != "delivery failed" || got[unavailable[0]] != "remote control unavailable" {
		t.Fatalf("batched results = %#v", got)
	}
	if got[clients[10]] != "" {
		t.Fatalf("untouched result was updated: %q", got[clients[10]])
	}
}

func TestCreateTaskRollsBackWhenAResultBatchFails(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_cleanup_time?mode=memory&cache=shared"
	db := dbcore.GetDBInstance()
	callbackName := "test:fail_remainder_task_results"
	batches := 0
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		table := tx.Statement.Table
		if table == "" && tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		if table != "task_results" {
			return
		}
		batches++
		if batches >= 2 {
			_ = tx.AddError(errInjectedTaskResultBatch)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() { db.Callback().Create().Remove(callbackName) })

	clients := make([]string, taskWriteChunkSize+1)
	for i := range clients {
		clients[i] = "rollback-" + itoa(i)
	}
	taskID := "task-rollback-batch"
	if err := CreateTask(taskID, clients, "uname"); err == nil {
		t.Fatal("CreateTask succeeded despite injected batch failure")
	}
	var tasks int64
	if err := db.Model(&models.Task{}).Where("task_id = ?", taskID).Count(&tasks).Error; err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	var results int64
	if err := db.Model(&models.TaskResult{}).Where("task_id = ?", taskID).Count(&results).Error; err != nil {
		t.Fatalf("count results: %v", err)
	}
	if tasks != 0 || results != 0 {
		t.Fatalf("partial write remained: tasks=%d results=%d", tasks, results)
	}
}

func TestSaveTaskResultsRetriesThenReturnsError(t *testing.T) {
	attempts := 0
	saveTaskResultsAttemptHook = func(attempt int) error {
		attempts++
		return errInjectedTaskResultBatch
	}
	t.Cleanup(func() { saveTaskResultsAttemptHook = nil })
	err := SaveTaskResults("task-retry", []string{"node-a"}, "delivery failed", -1, time.Now().UTC())
	if err == nil {
		t.Fatal("SaveTaskResults succeeded despite injected failures")
	}
	if attempts != saveTaskResultAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, saveTaskResultAttempts)
	}
}

func TestSaveTaskResultsSucceedsAfterTransientFailure(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_cleanup_time?mode=memory&cache=shared"
	_ = dbcore.GetDBInstance()
	taskID := "task-retry-ok"
	if err := CreateTask(taskID, []string{"node-ok"}, "uname"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	attempts := 0
	saveTaskResultsAttemptHook = func(attempt int) error {
		attempts++
		if attempt < 2 {
			return errInjectedTaskResultBatch
		}
		return nil
	}
	t.Cleanup(func() { saveTaskResultsAttemptHook = nil })
	if err := SaveTaskResults(taskID, []string{"node-ok"}, "delivery failed", -1, time.Now().UTC()); err != nil {
		t.Fatalf("SaveTaskResults: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	results, err := GetTaskResultsByTaskId(taskID)
	if err != nil {
		t.Fatalf("GetTaskResultsByTaskId: %v", err)
	}
	if len(results) != 1 || results[0].Result != "delivery failed" {
		t.Fatalf("results = %#v", results)
	}
}

func TestSaveIncomingTaskResultDoesNotOverwriteFinishedWithInterrupted(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_incoming_overwrite?mode=memory&cache=shared"
	if err := CreateTask("task-keep", []string{"node-a"}, "true"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	finishedAt := time.Now().UTC()
	if err := SaveIncomingTaskResult("task-keep", "node-a", "hello world", "finished", 0, finishedAt); err != nil {
		t.Fatalf("save finished: %v", err)
	}
	if err := SaveIncomingTaskResult("task-keep", "node-a", "execution status unknown", "interrupted", -1, time.Now().UTC()); err != nil {
		t.Fatalf("save interrupted: %v", err)
	}
	got, err := GetSpecificTaskResult("task-keep", "node-a")
	if err != nil {
		t.Fatalf("GetSpecificTaskResult: %v", err)
	}
	if got.Result != "hello world" {
		t.Fatalf("result overwritten: %#v", got.Result)
	}
}

func TestSaveIncomingTaskResultAllowsFinishedAfterTimeout(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_incoming_timeout?mode=memory&cache=shared"
	if err := CreateTask("task-timeout", []string{"node-b"}, "true"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := SaveIncomingTaskResult("task-timeout", "node-b", "delivery timeout", "", -1, time.Now().UTC()); err != nil {
		t.Fatalf("save timeout: %v", err)
	}
	if err := SaveIncomingTaskResult("task-timeout", "node-b", "ok", "finished", 0, time.Now().UTC()); err != nil {
		t.Fatalf("save finished: %v", err)
	}
	got, err := GetSpecificTaskResult("task-timeout", "node-b")
	if err != nil {
		t.Fatalf("GetSpecificTaskResult: %v", err)
	}
	if got.Result != "ok" {
		t.Fatalf("timeout was not replaced by finished result: %#v", got.Result)
	}
}

func TestSaveIncomingTaskResultConcurrentFinishedWins(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_incoming_race?mode=memory&cache=shared"
	if err := CreateTask("task-race", []string{"node-c"}, "true"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := SaveIncomingTaskResult("task-race", "node-c", "hello world", "finished", 0, time.Now().UTC()); err != nil {
			t.Errorf("save finished: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := SaveIncomingTaskResult("task-race", "node-c", "execution status unknown", "interrupted", -1, time.Now().UTC()); err != nil {
			t.Errorf("save interrupted: %v", err)
		}
	}()
	wg.Wait()
	got, err := GetSpecificTaskResult("task-race", "node-c")
	if err != nil {
		t.Fatalf("GetSpecificTaskResult: %v", err)
	}
	if got.Result != "hello world" {
		t.Fatalf("concurrent result = %#v, want finished output", got.Result)
	}
}

func TestCancelUndeliveredTaskResultWritesExplicitOutcome(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_cancel_undelivered?mode=memory&cache=shared"
	if err := CreateTask("task-cancel", []string{"node-a"}, "uname"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := CancelUndeliveredTaskResult("task-cancel", "node-a", "远程管理已关闭，任务未投递/已取消"); err != nil {
		t.Fatalf("CancelUndeliveredTaskResult: %v", err)
	}
	got, err := GetSpecificTaskResult("task-cancel", "node-a")
	if err != nil {
		t.Fatalf("GetSpecificTaskResult: %v", err)
	}
	if got.FinishedAt == nil {
		t.Fatal("cancelled task is still waiting")
	}
	if got.Result != "远程管理已关闭，任务未投递/已取消" {
		t.Fatalf("result = %q", got.Result)
	}
}

func TestCancelUndeliveredTaskResultDoesNotOverwriteFinished(t *testing.T) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:task_cancel_keep_finished?mode=memory&cache=shared"
	if err := CreateTask("task-done", []string{"node-b"}, "true"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	finishedAt := time.Now().UTC()
	if err := SaveIncomingTaskResult("task-done", "node-b", "hello world", "finished", 0, finishedAt); err != nil {
		t.Fatalf("SaveIncomingTaskResult: %v", err)
	}
	if err := CancelUndeliveredTaskResult("task-done", "node-b", "远程管理已关闭，任务未投递/已取消"); err != nil {
		t.Fatalf("CancelUndeliveredTaskResult: %v", err)
	}
	got, err := GetSpecificTaskResult("task-done", "node-b")
	if err != nil {
		t.Fatalf("GetSpecificTaskResult: %v", err)
	}
	if got.Result != "hello world" {
		t.Fatalf("finished result overwritten: %q", got.Result)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
