package metricstore

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nuomiiiii/lite/pkg/metric"
)

type OrphanCleanupResult struct {
	Entities        int
	PingTasks       int
	PingAssignments int
}

// CleanupOrphanedData removes history that is no longer addressable through
// the current clients, ping tasks, and client/task assignments.
func CleanupOrphanedData(ctx context.Context, validEntities map[string]struct{}, validPingAssignments map[uint]map[string]struct{}) (OrphanCleanupResult, error) {
	if err := storeOperations.Acquire(ctx); err != nil {
		return OrphanCleanupResult{}, fmt.Errorf("wait for metric store operations before orphan cleanup: %w", err)
	}
	defer storeOperations.Release()
	storeMu.RLock()
	defer storeMu.RUnlock()
	activeStore := store
	if activeStore == nil {
		return OrphanCleanupResult{}, fmt.Errorf("metric store not initialized")
	}

	result := OrphanCleanupResult{}
	entityIDs, err := activeStore.AllEntityIDs(ctx)
	if err != nil {
		return result, fmt.Errorf("list metric entities: %w", err)
	}
	for _, entityID := range entityIDs {
		if _, exists := validEntities[entityID]; exists {
			continue
		}
		if _, err := activeStore.DeleteEntity(ctx, entityID); err != nil {
			return result, fmt.Errorf("delete orphaned metric entity %s: %w", entityID, err)
		}
		deleteReportTrafficState(entityID)
		result.Entities++
	}

	orphanTaskTags := make(map[string]struct{})
	orphanAssignments := make(map[metric.EntityTagValue]struct{})
	for _, metricName := range pingMetricNames {
		pairs, err := activeStore.MetricEntityTagValues(ctx, metricName, "task_id")
		if err != nil {
			return result, fmt.Errorf("list %s client/task pairs: %w", metricName, err)
		}
		for _, pair := range pairs {
			taskID, parseErr := strconv.ParseUint(pair.TagValue, 10, strconv.IntSize)
			if parseErr == nil {
				assignedClients, taskExists := validPingAssignments[uint(taskID)]
				if taskExists {
					if _, assigned := assignedClients[pair.EntityID]; !assigned {
						orphanAssignments[pair] = struct{}{}
					}
					continue
				}
			}
			orphanTaskTags[pair.TagValue] = struct{}{}
		}
	}
	for taskTag := range orphanTaskTags {
		for _, metricName := range pingMetricNames {
			if _, err := activeStore.DeleteSeries(ctx, metric.Query{
				MetricName: metricName,
				Tags:       map[string]string{"task_id": taskTag},
			}); err != nil {
				return result, fmt.Errorf("delete orphaned ping task %s: %w", taskTag, err)
			}
		}
		result.PingTasks++
	}
	for assignment := range orphanAssignments {
		if _, deletedWithTask := orphanTaskTags[assignment.TagValue]; deletedWithTask {
			continue
		}
		if err := deletePingAssignmentRecords(ctx, activeStore, assignment.EntityID, assignment.TagValue); err != nil {
			return result, fmt.Errorf("delete orphaned ping assignment client %s task %s: %w", assignment.EntityID, assignment.TagValue, err)
		}
		result.PingAssignments++
	}
	return result, nil
}
