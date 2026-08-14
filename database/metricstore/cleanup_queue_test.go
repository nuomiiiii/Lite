package metricstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMetricCleanupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:metric-cleanup-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.MetricCleanupJob{}))
	return db
}

func TestMetricCleanupJobRetriesWithoutDuplicatingIntent(t *testing.T) {
	db := newMetricCleanupTestDB(t)
	require.NoError(t, EnqueueEntityCleanup(db, "client-a"))
	require.NoError(t, EnqueueEntityCleanup(db, "client-a"))
	var count int64
	require.NoError(t, db.Model(&models.MetricCleanupJob{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	failure := errors.New("metric store unavailable")
	err := processPendingCleanupJobsWith(context.Background(), db, func(context.Context, models.MetricCleanupJob) error {
		return failure
	})
	require.ErrorIs(t, err, failure)
	var job models.MetricCleanupJob
	require.NoError(t, db.First(&job).Error)
	assert.Equal(t, 1, job.Attempts)
	assert.Contains(t, job.LastError, failure.Error())

	var executed []models.MetricCleanupJob
	require.NoError(t, processPendingCleanupJobsWith(context.Background(), db, func(_ context.Context, job models.MetricCleanupJob) error {
		executed = append(executed, job)
		return nil
	}))
	require.Len(t, executed, 1)
	require.NoError(t, db.Model(&models.MetricCleanupJob{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestMetricCleanupIntentRollsBackWithBusinessTransaction(t *testing.T) {
	db := newMetricCleanupTestDB(t)
	failure := errors.New("business delete rejected")
	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, EnqueuePingAssignmentCleanup(tx, PingAssignment{Client: "client-a", TaskID: 7}))
		return failure
	})
	require.ErrorIs(t, err, failure)
	var count int64
	require.NoError(t, db.Model(&models.MetricCleanupJob{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestPingAssignmentCleanupBlocksWritesUntilSuccessfulRetry(t *testing.T) {
	db := newMetricCleanupTestDB(t)
	assignment := PingAssignment{Client: "client-a", TaskID: 7}
	UnblockPingAssignmentWrites([]PingAssignment{assignment})
	t.Cleanup(func() { UnblockPingAssignmentWrites([]PingAssignment{assignment}) })
	require.NoError(t, EnqueuePingAssignmentCleanup(db, assignment))

	failure := errors.New("metric store unavailable")
	err := processPendingCleanupJobsWith(context.Background(), db, func(context.Context, models.MetricCleanupJob) error {
		return failure
	})
	require.ErrorIs(t, err, failure)
	assert.True(t, PingAssignmentWritesBlocked(assignment))
	require.ErrorIs(t, WritePingRecord(context.Background(), models.PingRecord{
		Client: assignment.Client,
		TaskId: assignment.TaskID,
		Time:   time.Now().UTC(),
	}), ErrMetricWriteBlocked)

	require.NoError(t, processPendingCleanupJobsWith(context.Background(), db, func(context.Context, models.MetricCleanupJob) error {
		return nil
	}))
	assert.False(t, PingAssignmentWritesBlocked(assignment))
	var count int64
	require.NoError(t, db.Model(&models.MetricCleanupJob{}).Count(&count).Error)
	assert.Zero(t, count)
}
