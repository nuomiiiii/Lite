package metricstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/metric"
	v1 "github.com/nuomiiiii/lite/protocol/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupOrphanedDataRemovesDeletedEntitiesAndPingTasks(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	now := time.Now().UTC()
	require.NoError(t, s.WriteBatch(ctx, []metric.Point{
		{MetricName: MetricCPU, EntityID: "client-a", Timestamp: now, Value: 10},
		{MetricName: MetricCPU, EntityID: "deleted-client", Timestamp: now, Value: 20},
		{MetricName: MetricPingLatency, EntityID: "client-a", Timestamp: now, Value: 12, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLoss, EntityID: "client-a", Timestamp: now, Value: 0, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-a", Timestamp: now, Value: 50, Tags: map[string]string{"task_id": "999"}},
		{MetricName: MetricPingLoss, EntityID: "client-a", Timestamp: now, Value: 1, Tags: map[string]string{"task_id": "999"}},
	}))

	result, err := CleanupOrphanedData(ctx,
		map[string]struct{}{"client-a": {}},
		map[uint]map[string]struct{}{1: {"client-a": {}}},
	)
	require.NoError(t, err)
	assert.Equal(t, OrphanCleanupResult{Entities: 1, PingTasks: 1}, result)

	entityIDs, err := s.AllEntityIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"client-a"}, entityIDs)
	taskIDs, err := s.MetricTagValues(ctx, MetricPingLatency, "task_id")
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, taskIDs)
}

func TestCleanupOrphanedDataRemovesOnlyStalePingAssignments(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	now := time.Now().UTC()
	require.NoError(t, s.WriteBatch(ctx, []metric.Point{
		{MetricName: MetricPingLatency, EntityID: "client-a", Timestamp: now, Value: 10, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-b", Timestamp: now, Value: 20, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-c", Timestamp: now, Value: 30, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-b", Timestamp: now, Value: 40, Tags: map[string]string{"task_id": "2"}},
	}))

	validEntities := map[string]struct{}{"client-a": {}, "client-b": {}, "client-c": {}}
	validAssignments := map[uint]map[string]struct{}{
		1: {"client-a": {}, "client-c": {}},
		2: {"client-b": {}},
	}
	result, err := CleanupOrphanedData(ctx, validEntities, validAssignments)
	require.NoError(t, err)
	assert.Equal(t, OrphanCleanupResult{PingAssignments: 1}, result)

	assertPingSeriesPoints(t, s, "client-a", 1, now, 1)
	assertPingSeriesPoints(t, s, "client-b", 1, now, 0)
	assertPingSeriesPoints(t, s, "client-c", 1, now, 1)
	assertPingSeriesPoints(t, s, "client-b", 2, now, 1)

	result, err = CleanupOrphanedData(ctx, validEntities, validAssignments)
	require.NoError(t, err)
	assert.Equal(t, OrphanCleanupResult{}, result)
	assertPingSeriesPoints(t, s, "client-a", 1, now, 1)
	assertPingSeriesPoints(t, s, "client-c", 1, now, 1)
	assertPingSeriesPoints(t, s, "client-b", 2, now, 1)
}

func TestDeletePingRecordsByAssignmentsIsPreciselyScoped(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	now := time.Now().UTC()
	require.NoError(t, s.WriteBatch(ctx, []metric.Point{
		{MetricName: MetricPingLatency, EntityID: "client-a", Timestamp: now, Value: 10, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-b", Timestamp: now, Value: 20, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-c", Timestamp: now, Value: 30, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-b", Timestamp: now, Value: 40, Tags: map[string]string{"task_id": "2"}},
	}))

	require.NoError(t, DeletePingRecordsByAssignments(ctx, []PingAssignment{{Client: "client-b", TaskID: 1}}))
	assertPingSeriesPoints(t, s, "client-a", 1, now, 1)
	assertPingSeriesPoints(t, s, "client-b", 1, now, 0)
	assertPingSeriesPoints(t, s, "client-c", 1, now, 1)
	assertPingSeriesPoints(t, s, "client-b", 2, now, 1)
}

func TestDeletePingRecordsByTaskKeepsOtherTasks(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	now := time.Now().UTC()
	require.NoError(t, s.WriteBatch(ctx, []metric.Point{
		{MetricName: MetricPingLatency, EntityID: "client-a", Timestamp: now, Value: 10, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-b", Timestamp: now, Value: 20, Tags: map[string]string{"task_id": "1"}},
		{MetricName: MetricPingLatency, EntityID: "client-b", Timestamp: now, Value: 40, Tags: map[string]string{"task_id": "2"}},
	}))

	require.NoError(t, DeletePingRecordsByTask(ctx, []uint{1}))
	assertPingSeriesPoints(t, s, "client-a", 1, now, 0)
	assertPingSeriesPoints(t, s, "client-b", 1, now, 0)
	assertPingSeriesPoints(t, s, "client-b", 2, now, 1)
}

func assertPingSeriesPoints(t *testing.T, s *metric.Store, client string, taskID uint, now time.Time, want int) {
	t.Helper()
	points, err := s.Query(context.Background(), metric.Query{
		MetricName: MetricPingLatency,
		EntityID:   client,
		Tags:       map[string]string{"task_id": fmt.Sprintf("%d", taskID)},
		Start:      now.Add(-time.Second),
		End:        now.Add(time.Second),
	})
	require.NoError(t, err)
	assert.Len(t, points, want)
}

func TestBlockedTargetsCannotRecreateDeletedMetrics(t *testing.T) {
	ctx := context.Background()
	s := useReportTestStore(t, nil)
	StartReportBatcher()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = StopReportBatcher(stopCtx)
	})

	report := v1.Report{UUID: "deleted-client", UpdatedAt: time.Now().UTC(), CPU: v1.CPUReport{Usage: 25}}
	_, err := WriteReport(ctx, report)
	require.NoError(t, err)
	BlockEntityWrites(report.UUID)
	t.Cleanup(func() { UnblockEntityWrites(report.UUID) })
	require.NoError(t, FlushReportBatch(ctx))

	points, err := s.Query(ctx, metric.Query{
		MetricName: MetricCPU,
		EntityID:   report.UUID,
		Start:      report.UpdatedAt.Add(-time.Second),
		End:        report.UpdatedAt.Add(time.Second),
	})
	require.NoError(t, err)
	assert.Empty(t, points)
	_, err = WriteReport(ctx, report)
	assert.ErrorIs(t, err, ErrMetricWriteBlocked)

	BlockPingTaskWrites([]uint{99})
	t.Cleanup(func() { UnblockPingTaskWrites([]uint{99}) })
	err = WritePingRecord(ctx, models.PingRecord{Client: "client-a", TaskId: 99, Time: time.Now().UTC(), Value: 10})
	assert.True(t, errors.Is(err, ErrMetricWriteBlocked))
}
