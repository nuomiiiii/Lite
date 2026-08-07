package metric

import "context"

type metricBatchScanObserverKey struct{}

func withMetricBatchScanObserver(ctx context.Context, observer func(string)) context.Context {
	return context.WithValue(ctx, metricBatchScanObserverKey{}, observer)
}

func observeMetricBatchScan(ctx context.Context, kind string) {
	observer, _ := ctx.Value(metricBatchScanObserverKey{}).(func(string))
	if observer != nil {
		observer(kind)
	}
}
