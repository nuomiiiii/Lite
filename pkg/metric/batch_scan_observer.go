package metric

import "context"

type metricBatchScanObserverKey struct{}

func observeMetricBatchScan(ctx context.Context, kind string) {
	observer, _ := ctx.Value(metricBatchScanObserverKey{}).(func(string))
	if observer != nil {
		observer(kind)
	}
}
