package metricstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	logger "github.com/nuomiiiii/lite/utils/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var metricCleanupMu sync.Mutex

func EnqueueEntityCleanup(tx *gorm.DB, entityID string) error {
	if entityID == "" {
		return fmt.Errorf("metric cleanup entity ID is required")
	}
	return enqueueMetricCleanup(tx, models.MetricCleanupJob{Kind: models.MetricCleanupEntity, EntityID: entityID})
}

func EnqueuePingTaskCleanup(tx *gorm.DB, taskID uint) error {
	if taskID == 0 {
		return fmt.Errorf("metric cleanup ping task ID is required")
	}
	return enqueueMetricCleanup(tx, models.MetricCleanupJob{Kind: models.MetricCleanupPingTask, TaskID: taskID})
}

func EnqueuePingAssignmentCleanup(tx *gorm.DB, assignment PingAssignment) error {
	if assignment.Client == "" || assignment.TaskID == 0 {
		return fmt.Errorf("metric cleanup ping assignment is incomplete")
	}
	return enqueueMetricCleanup(tx, models.MetricCleanupJob{
		Kind: models.MetricCleanupPingAssignment, Client: assignment.Client, TaskID: assignment.TaskID,
	})
}

func enqueueMetricCleanup(tx *gorm.DB, job models.MetricCleanupJob) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}, {Name: "entity_id"}, {Name: "task_id"}, {Name: "client"}},
		DoNothing: true,
	}).Create(&job).Error
}

func ProcessPendingCleanupJobs(ctx context.Context, db *gorm.DB) error {
	return processPendingCleanupJobsWith(ctx, db, executeMetricCleanupJob)
}

func processPendingCleanupJobsWith(ctx context.Context, db *gorm.DB, execute func(context.Context, models.MetricCleanupJob) error) error {
	metricCleanupMu.Lock()
	defer metricCleanupMu.Unlock()

	var jobs []models.MetricCleanupJob
	if err := db.WithContext(ctx).Order("id ASC").Find(&jobs).Error; err != nil {
		return fmt.Errorf("list pending metric cleanup jobs: %w", err)
	}
	var cleanupErrors []error
	for _, job := range jobs {
		blockPendingCleanupWrites(job)
		if err := execute(ctx, job); err != nil {
			message := err.Error()
			if updateErr := db.WithContext(ctx).Model(&models.MetricCleanupJob{}).Where("id = ?", job.ID).Updates(map[string]any{
				"attempts": gorm.Expr("attempts + 1"), "last_error": message,
			}).Error; updateErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("metric cleanup job %d failed: %v; record failure: %w", job.ID, err, updateErr))
			} else {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("metric cleanup job %d: %w", job.ID, err))
			}
			continue
		}
		if err := db.WithContext(ctx).Delete(&models.MetricCleanupJob{}, job.ID).Error; err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove completed metric cleanup job %d: %w", job.ID, err))
			continue
		}
		unblockCompletedCleanupWrites(job)
	}
	return errors.Join(cleanupErrors...)
}

func blockPendingCleanupWrites(job models.MetricCleanupJob) {
	switch job.Kind {
	case models.MetricCleanupEntity:
		BlockEntityWrites(job.EntityID)
	case models.MetricCleanupPingTask:
		BlockPingTaskWrites([]uint{job.TaskID})
	case models.MetricCleanupPingAssignment:
		BlockPingAssignmentWrites([]PingAssignment{{Client: job.Client, TaskID: job.TaskID}})
	}
}

func unblockCompletedCleanupWrites(job models.MetricCleanupJob) {
	if job.Kind == models.MetricCleanupPingAssignment {
		UnblockPingAssignmentWrites([]PingAssignment{{Client: job.Client, TaskID: job.TaskID}})
	}
}

func executeMetricCleanupJob(ctx context.Context, job models.MetricCleanupJob) error {
	switch job.Kind {
	case models.MetricCleanupEntity:
		return DeleteEntity(ctx, job.EntityID)
	case models.MetricCleanupPingTask:
		return DeletePingRecordsByTask(ctx, []uint{job.TaskID})
	case models.MetricCleanupPingAssignment:
		return DeletePingRecordsByAssignments(ctx, []PingAssignment{{Client: job.Client, TaskID: job.TaskID}})
	default:
		return fmt.Errorf("unsupported metric cleanup kind %q", job.Kind)
	}
}

func StartPendingCleanupWorker(db *gorm.DB) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := ProcessPendingCleanupJobs(ctx, db); err != nil && !errors.Is(err, context.Canceled) {
					logger.Errorf("metricstore", "Pending metric cleanup retry failed: %v", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
