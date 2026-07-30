package jsonrpc

import (
	"context"
	"sync"
	"time"

	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/metricstore"
	"github.com/komari-monitor/komari/pkg/metric"
	"github.com/komari-monitor/komari/pkg/rpc"
)

const (
	databaseLocationLocal    = "local"
	databaseLocationExternal = "external"
)

var databaseMaintenanceMu sync.Mutex

type databaseFileSizes struct {
	Database int64 `json:"database"`
	WAL      int64 `json:"wal"`
	SHM      int64 `json:"shm"`
}

func (sizes databaseFileSizes) total() int64 {
	return sizes.Database + sizes.WAL + sizes.SHM
}

type databaseStorageStatus struct {
	Driver   string                 `json:"driver"`
	Location string                 `json:"location"`
	Size     *int64                 `json:"size"`
	Files    *databaseFileSizes     `json:"files,omitempty"`
	Runtime  *databaseRuntimeStatus `json:"runtime,omitempty"`
	Action   string                 `json:"action"`
	Error    string                 `json:"error,omitempty"`
}

type databaseDigestHandoffStatus struct {
	Metric string     `json:"metric"`
	Reason string     `json:"reason"`
	At     *time.Time `json:"at"`
}

type databaseRuntimeStatus struct {
	Compacting                    bool                          `json:"compacting"`
	CurrentMetric                 string                        `json:"current_metric"`
	Progress                      int                           `json:"progress"`
	Total                         int                           `json:"total"`
	CycleWritten                  int                           `json:"cycle_written"`
	CycleStartedAt                *time.Time                    `json:"cycle_started_at"`
	LastStepAt                    *time.Time                    `json:"last_step_at"`
	LastCycleCompletedAt          *time.Time                    `json:"last_cycle_completed_at"`
	CheckpointApplicable          bool                          `json:"checkpoint_applicable"`
	LastCheckpointAttemptAt       *time.Time                    `json:"last_checkpoint_attempt_at"`
	LastCheckpointSuccessAt       *time.Time                    `json:"last_checkpoint_success_at"`
	NextCheckpointAt              *time.Time                    `json:"next_checkpoint_at"`
	CheckpointPending             bool                          `json:"checkpoint_pending"`
	ConsecutiveCheckpointFailures int                           `json:"consecutive_checkpoint_failures"`
	ConsecutiveCycleFailures      int                           `json:"consecutive_cycle_failures"`
	LastError                     string                        `json:"last_error,omitempty"`
	DigestHandoffDeferred         []databaseDigestHandoffStatus `json:"digest_handoff_deferred"`
}

type databaseStatusResponse struct {
	// Type and Size preserve the original main-database-only response contract.
	Type       string                `json:"type"`
	Size       int64                 `json:"size"`
	Main       databaseStorageStatus `json:"main"`
	Monitoring databaseStorageStatus `json:"monitoring"`
	LocalTotal *int64                `json:"local_total"`
}

type databaseMaintenanceResult struct {
	Driver    string `json:"driver"`
	Action    string `json:"action"`
	Before    *int64 `json:"before"`
	After     *int64 `json:"after"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	SizeError string `json:"size_error,omitempty"`
}

type databaseMaintenanceResponse struct {
	// Before, After and Size preserve the original main-database-only fields.
	Before       int64                     `json:"before"`
	After        int64                     `json:"after"`
	Size         int64                     `json:"size"`
	AllSucceeded bool                      `json:"all_succeeded"`
	Main         databaseMaintenanceResult `json:"main"`
	Monitoring   databaseMaintenanceResult `json:"monitoring"`
}

func init() {
	// Retain the original method names so existing REST/RPC clients keep working;
	// the result now describes both databases and their driver-specific actions.
	reg("getDatabaseSize", adminGetDatabaseSize, "Get main and monitoring database storage usage")
	reg("vacuumDatabase", adminVacuumDatabase, "Reclaim space in the main and monitoring databases")
}

func adminGetDatabaseSize(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	main := mainDatabaseStatus()
	monitoring := monitoringDatabaseStatus(ctx)
	legacySize := int64(0)
	if main.Size != nil {
		legacySize = *main.Size
	}

	return databaseStatusResponse{
		Type:       main.Driver,
		Size:       legacySize,
		Main:       main,
		Monitoring: monitoring,
		LocalTotal: localDatabaseTotal(main, monitoring),
	}, nil
}

func adminVacuumDatabase(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	if !databaseMaintenanceMu.TryLock() {
		return nil, rpc.MakeError(rpc.InternalError, "database maintenance is already in progress", nil)
	}
	defer databaseMaintenanceMu.Unlock()

	main := maintainMainDatabase(ctx)
	monitoring := maintainMonitoringDatabase(ctx)
	response := newDatabaseMaintenanceResponse(main, monitoring)

	actor, ip := auditActor(ctx)
	level := "warn"
	message := "reclaimed database space"
	if !response.AllSucceeded {
		message = "database space reclaim completed with errors"
		level = "error"
	}
	auditlog.Log(ip, actor, message, level)

	return response, nil
}

func newDatabaseMaintenanceResponse(main, monitoring databaseMaintenanceResult) databaseMaintenanceResponse {
	return databaseMaintenanceResponse{
		Before:       valueOrZero(main.Before),
		After:        valueOrZero(main.After),
		Size:         valueOrZero(main.After),
		AllSucceeded: main.Success && monitoring.Success,
		Main:         main,
		Monitoring:   monitoring,
	}
}

func mainDatabaseStatus() databaseStorageStatus {
	status := databaseStorageStatus{
		Driver:   flags.NormalizeDatabaseType(flags.DatabaseType),
		Location: databaseLocationLocal,
		Action:   string(metric.MaintenanceVacuum),
	}
	files, err := dbcore.StorageFiles()
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Files = &databaseFileSizes{
		Database: files.Database,
		WAL:      files.WAL,
		SHM:      files.SHM,
	}
	status.Size = int64Pointer(status.Files.total())
	return status
}

func monitoringDatabaseStatus(ctx context.Context) databaseStorageStatus {
	info, err := metricstore.InspectStorage(ctx)
	status := databaseStorageStatus{
		Driver: string(info.Driver),
		Action: string(info.Action),
	}
	status.Location = databaseLocationForDriver(info.Driver)
	if info.Driver != "" {
		status.Runtime = newDatabaseRuntimeStatus(info.Driver, metricstore.GetRuntimeStatus())
	}
	if err != nil {
		status.Error = err.Error()
		return status
	}
	if info.Files != nil {
		status.Files = &databaseFileSizes{
			Database: info.Files.Database,
			WAL:      info.Files.WAL,
			SHM:      info.Files.SHM,
		}
	}
	status.Size = int64Pointer(info.Size)
	return status
}

func newDatabaseRuntimeStatus(driver metric.Driver, runtime metricstore.RuntimeStatus) *databaseRuntimeStatus {
	status := &databaseRuntimeStatus{
		Compacting:                    runtime.Compacting,
		CurrentMetric:                 runtime.CurrentMetric,
		Progress:                      runtime.Progress,
		Total:                         runtime.Total,
		CycleWritten:                  runtime.CycleWritten,
		CycleStartedAt:                nonZeroTimePointer(runtime.CycleStartedAt),
		LastStepAt:                    nonZeroTimePointer(runtime.LastStepAt),
		LastCycleCompletedAt:          nonZeroTimePointer(runtime.LastCycleCompletedAt),
		CheckpointApplicable:          driver == metric.DriverSQLite,
		CheckpointPending:             runtime.CheckpointPending,
		ConsecutiveCheckpointFailures: runtime.ConsecutiveCheckpointFailures,
		ConsecutiveCycleFailures:      runtime.ConsecutiveCycleFailures,
		LastError:                     runtime.LastError,
		DigestHandoffDeferred:         make([]databaseDigestHandoffStatus, 0, len(runtime.DigestHandoffDeferred)),
	}
	if status.CheckpointApplicable {
		status.LastCheckpointAttemptAt = nonZeroTimePointer(runtime.LastCheckpointAttemptAt)
		status.LastCheckpointSuccessAt = nonZeroTimePointer(runtime.LastCheckpointSuccessAt)
		status.NextCheckpointAt = nonZeroTimePointer(runtime.NextCheckpointAt)
	}
	for _, deferred := range runtime.DigestHandoffDeferred {
		status.DigestHandoffDeferred = append(status.DigestHandoffDeferred, databaseDigestHandoffStatus{
			Metric: deferred.Metric,
			Reason: deferred.Reason,
			At:     nonZeroTimePointer(deferred.At),
		})
	}
	return status
}

func maintainMainDatabase(ctx context.Context) databaseMaintenanceResult {
	status := mainDatabaseStatus()
	result := databaseMaintenanceResult{
		Driver: status.Driver,
		Action: status.Action,
		Before: status.Size,
	}
	if status.Error != "" {
		result.SizeError = "before: " + status.Error
	}

	if !flags.IsSQLite() {
		result.Error = "main database maintenance is only supported for SQLite"
	} else if err := dbcore.ReclaimSpace(ctx); err != nil {
		result.Error = err.Error()
	} else {
		result.Success = true
	}

	after, err := dbcore.StorageSize()
	if err != nil {
		result.SizeError = appendMeasurementError(result.SizeError, "after", err)
	} else {
		result.After = int64Pointer(after)
	}
	return result
}

func maintainMonitoringDatabase(ctx context.Context) databaseMaintenanceResult {
	maintained, maintenanceErr := metricstore.ReclaimSpace(ctx)
	result := databaseMaintenanceResult{
		Driver:  string(maintained.Driver),
		Action:  string(maintained.Action),
		Success: maintenanceErr == nil,
	}
	if maintained.BeforeSizeError != nil {
		result.SizeError = appendMeasurementError(result.SizeError, "before", maintained.BeforeSizeError)
	} else {
		result.Before = int64Pointer(maintained.Before)
	}
	if maintained.AfterSizeError != nil {
		result.SizeError = appendMeasurementError(result.SizeError, "after", maintained.AfterSizeError)
	} else {
		result.After = int64Pointer(maintained.After)
	}
	if maintenanceErr != nil {
		result.Error = maintenanceErr.Error()
	}
	return result
}

func databaseLocationForDriver(driver metric.Driver) string {
	if driver == metric.DriverSQLite {
		return databaseLocationLocal
	}
	if driver == "" {
		return ""
	}
	return databaseLocationExternal
}

func localDatabaseTotal(statuses ...databaseStorageStatus) *int64 {
	var total int64
	for _, status := range statuses {
		if status.Location != databaseLocationLocal || status.Size == nil {
			return nil
		}
		total += *status.Size
	}
	return int64Pointer(total)
}

func appendMeasurementError(current, phase string, err error) string {
	next := phase + ": " + err.Error()
	if current == "" {
		return next
	}
	return current + "; " + next
}

func int64Pointer(value int64) *int64 {
	return &value
}

func nonZeroTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
