package metricstore

import (
	"testing"

	"github.com/nuomiiiii/lite/pkg/metric"
)

func TestPingLossStatsFromPoints(t *testing.T) {
	stats := PingLossStatsFromPoints([]metric.AggregatePoint{
		{Value: 0.1, Count: 20},
		{Value: 0.25, Count: 4},
	})
	if stats.Total != 24 || stats.Lost != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	if got := stats.LossRate(); got < 12.499 || got > 12.501 {
		t.Fatalf("loss rate = %v", got)
	}
	full := PingLossStatsFromPoints([]metric.AggregatePoint{{Value: 1, Count: 2}})
	if full.Total != 2 || full.Lost != 2 || full.LossRatio() != 1 {
		t.Fatalf("full loss = %+v", full)
	}
}
