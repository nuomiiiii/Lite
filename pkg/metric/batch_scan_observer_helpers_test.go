package metric

import "context"

func withMetricBatchScanObserver(ctx context.Context, observer func(string)) context.Context {
	return context.WithValue(ctx, metricBatchScanObserverKey{}, observer)
}
