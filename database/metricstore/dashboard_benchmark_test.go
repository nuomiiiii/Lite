package metricstore

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/komari-monitor/komari/pkg/metric"
)

var dashboardTrafficBenchmarkRecords int
var dashboardLatencyBenchmarkPoints int

func BenchmarkDashboardTrafficRead30(b *testing.B) {
	benchmarkDashboardTrafficRead(b, 30, false)
}

func BenchmarkDashboardTrafficRead80(b *testing.B) {
	benchmarkDashboardTrafficRead(b, 80, false)
}

func BenchmarkDashboardTrafficHybridRead30(b *testing.B) {
	benchmarkDashboardTrafficRead(b, 30, true)
}

func BenchmarkDashboardTrafficHybridRead80(b *testing.B) {
	benchmarkDashboardTrafficRead(b, 80, true)
}

func BenchmarkDashboardLatencyRead30(b *testing.B) {
	benchmarkDashboardLatencyRead(b, 30)
}

func BenchmarkDashboardLatencyRead80(b *testing.B) {
	benchmarkDashboardLatencyRead(b, 80)
}

func benchmarkDashboardTrafficRead(b *testing.B, nodeCount int, compact bool) {
	b.Helper()
	ctx := context.Background()
	dsn := fmt.Sprintf("file:dashboard-benchmark-%d-%d?mode=memory&cache=shared", nodeCount, time.Now().UnixNano())
	options := []metric.Option{metric.WithMaxOpenConns(1)}
	if compact {
		options = append(options, metric.WithRollupPolicy(defaultRollupPolicy()))
	}
	s, err := metric.Open(ctx, metric.SQLite(dsn, options...))
	if err != nil {
		b.Fatalf("open benchmark store: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := createMetricDefinitions(ctx, s); err != nil {
		b.Fatalf("create benchmark metrics: %v", err)
	}
	storeMu.Lock()
	previousStore := store
	store = s
	storeMu.Unlock()
	b.Cleanup(func() {
		storeMu.Lock()
		store = previousStore
		storeMu.Unlock()
	})

	end := time.Now().UTC().Truncate(5 * time.Minute)
	start := end.Add(-24 * time.Hour)
	clientIDs := make([]string, nodeCount)
	points := make([]metric.Point, 0, nodeCount*4*289)
	for node := 0; node < nodeCount; node++ {
		clientID := fmt.Sprintf("node-%03d", node)
		clientIDs[node] = clientID
		baseUp := float64(1_000_000_000 + node*10_000_000)
		baseDown := baseUp * 2
		points = append(points,
			metric.Point{MetricName: MetricNetTotalUp, EntityID: clientID, Timestamp: start.Add(-5 * time.Minute), Value: baseUp},
			metric.Point{MetricName: MetricNetTotalDown, EntityID: clientID, Timestamp: start.Add(-5 * time.Minute), Value: baseDown},
		)
		for sample := 0; sample < 288; sample++ {
			ts := start.Add(time.Duration(sample) * 5 * time.Minute)
			up := float64(2_000_000 + node*1000 + sample)
			down := float64(4_000_000 + node*2000 + sample*2)
			points = append(points,
				metric.Point{MetricName: MetricNetTotalUp, EntityID: clientID, Timestamp: ts, Value: baseUp + float64(sample+1)*up},
				metric.Point{MetricName: MetricNetTotalDown, EntityID: clientID, Timestamp: ts, Value: baseDown + float64(sample+1)*down},
				metric.Point{MetricName: MetricTrafficUp, EntityID: clientID, Timestamp: ts, Value: up},
				metric.Point{MetricName: MetricTrafficDown, EntityID: clientID, Timestamp: ts, Value: down},
			)
		}
	}
	if err := s.WriteBatch(ctx, points); err != nil {
		b.Fatalf("seed benchmark points: %v", err)
	}
	if compact {
		if _, err := s.Compact(ctx, end); err != nil {
			b.Fatalf("compact benchmark points: %v", err)
		}
	}
	points = nil
	runtime.GC()

	b.Run("legacy-per-node", func(b *testing.B) {
		b.ReportAllocs()
		for iteration := 0; iteration < b.N; iteration++ {
			total := 0
			for _, clientID := range clientIDs {
				records, err := GetTrafficRecordsByClientAndTime(ctx, clientID, start, end)
				if err != nil {
					b.Fatal(err)
				}
				total += len(records)
				if _, err := GetLatestTrafficBefore(ctx, []string{clientID}, start); err != nil {
					b.Fatal(err)
				}
			}
			dashboardTrafficBenchmarkRecords = total
		}
	})

	b.Run("batched", func(b *testing.B) {
		b.ReportAllocs()
		for iteration := 0; iteration < b.N; iteration++ {
			records, _, err := GetTrafficRecordsByClientsAndTime(ctx, clientIDs, start, end)
			if err != nil {
				b.Fatal(err)
			}
			dashboardTrafficBenchmarkRecords = len(records)
		}
	})
}

func benchmarkDashboardLatencyRead(b *testing.B, nodeCount int) {
	b.Helper()
	ctx := context.Background()
	dsn := fmt.Sprintf("file:dashboard-latency-benchmark-%d-%d?mode=memory&cache=shared", nodeCount, time.Now().UnixNano())
	s, err := metric.Open(ctx, metric.SQLite(dsn, metric.WithMaxOpenConns(1)))
	if err != nil {
		b.Fatalf("open latency benchmark store: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := createMetricDefinitions(ctx, s); err != nil {
		b.Fatalf("create latency benchmark metrics: %v", err)
	}

	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-6 * time.Hour)
	clientIDs := make([]string, nodeCount)
	points := make([]metric.Point, 0, nodeCount*2*361)
	for node := 0; node < nodeCount; node++ {
		clientID := fmt.Sprintf("node-%03d", node)
		clientIDs[node] = clientID
		for task := 0; task < 2; task++ {
			tags := map[string]string{"task_id": fmt.Sprintf("task-%d", task)}
			for sample := 0; sample <= 360; sample++ {
				value := float64(20 + node + task*5 + sample%17)
				if sample%41 == 0 {
					value = -1
				}
				points = append(points, metric.Point{
					MetricName: MetricPingLatency, EntityID: clientID,
					Timestamp: start.Add(time.Duration(sample) * time.Minute), Value: value, Tags: tags,
				})
			}
		}
	}
	if err := s.WriteBatch(ctx, points); err != nil {
		b.Fatalf("seed latency benchmark points: %v", err)
	}
	points = nil
	runtime.GC()

	query := metric.AggregateQuery{
		Query:       metric.Query{MetricName: MetricPingLatency, Start: start, End: end, Order: metric.OrderAsc},
		Aggregation: metric.AggAvg, Interval: time.Hour,
	}
	b.Run("legacy-per-node-full-summary", func(b *testing.B) {
		b.ReportAllocs()
		for iteration := 0; iteration < b.N; iteration++ {
			total := 0
			for _, clientID := range clientIDs {
				perNode := query
				perNode.EntityID = clientID
				summary, err := s.PingSeriesSummary(ctx, perNode, end)
				if err != nil {
					b.Fatal(err)
				}
				total += len(summary.Avg)
			}
			dashboardLatencyBenchmarkPoints = total
		}
	})
	b.Run("batched-average-only", func(b *testing.B) {
		b.ReportAllocs()
		batched := query
		batched.PreserveSeries = true
		for iteration := 0; iteration < b.N; iteration++ {
			series, err := s.DashboardSeries(ctx, batched, end)
			if err != nil {
				b.Fatal(err)
			}
			dashboardLatencyBenchmarkPoints = len(series)
		}
	})
}
