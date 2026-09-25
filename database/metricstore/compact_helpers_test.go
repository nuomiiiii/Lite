package metricstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/pkg/metric"
)

func Compact(ctx context.Context, now time.Time) (int, error) {
	if !compactOperations.TryAcquire() {
		return 0, ErrCompactInProgress
	}
	defer compactOperations.Release()
	if err := storeOperations.AcquireShared(ctx); err != nil {
		return 0, fmt.Errorf("wait for metric store operation before compaction: %w", err)
	}
	defer storeOperations.ReleaseShared()

	storeMu.RLock()
	defer storeMu.RUnlock()
	activeStore := store
	if activeStore == nil {
		return 0, fmt.Errorf("metric store not initialized")
	}

	defs, err := activeStore.ListMetrics(ctx)
	if err != nil {
		return 0, err
	}
	defs = compactableMetricDefinitions(activeStore, defs)
	if len(defs) == 0 {
		compactAt = 0
		return 0, nil
	}
	if compactAt >= len(defs) {
		compactAt = 0
	}

	total := 0
	start := compactAt
	failedAt := -1
	var compactErrors []error
	for i := 0; i < len(defs); i++ {
		idx := (start + i) % len(defs)
		metricName := defs[idx].Name
		n, err := activeStore.CompactMetric(ctx, metricName, now)
		if metric.IsDigestHandoffDeferred(err) {
			handleDigestHandoffDeferred(metricName, err, time.Now().UTC())
			continue
		}
		if err != nil {
			if failedAt < 0 {
				failedAt = idx
			}
			compactErrors = append(compactErrors, fmt.Errorf("compact metric %q: %w", metricName, err))
			continue
		}
		clearDigestHandoffDeferred(metricName)
		total += n
	}
	if err := finishCompactCycle(ctx, activeStore, now, true); err != nil {
		compactErrors = append(compactErrors, err)
	}
	if failedAt >= 0 {
		compactAt = failedAt
	} else {
		compactAt = start
	}
	return total, errors.Join(compactErrors...)
}
