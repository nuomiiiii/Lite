package metricstore

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/komari-monitor/komari/pkg/metric"
)

func TestDashboardSQLiteResourceAcceptance(t *testing.T) {
	if os.Getenv("KOMARI_DASHBOARD_PERF") != "1" {
		t.Skip("set KOMARI_DASHBOARD_PERF=1 to run the dashboard resource acceptance test")
	}
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })

	for _, test := range []struct {
		nodes         int
		maxHeapGrowth uint64
	}{
		{nodes: 30, maxHeapGrowth: 2 << 20},
		{nodes: 80, maxHeapGrowth: 3 << 20},
	} {
		t.Run(fmt.Sprintf("nodes-%d", test.nodes), func(t *testing.T) {
			ctx := context.Background()
			store := openDashboardResourceFixture(t, ctx, test.nodes)
			clientIDs, start, end := seedDashboardResourceFixture(t, ctx, store, test.nodes)

			storeMu.Lock()
			previousStore := metricStore()
			setMetricStore(store)
			storeMu.Unlock()
			t.Cleanup(func() {
				storeMu.Lock()
				setMetricStore(previousStore)
				storeMu.Unlock()
			})

			query := metric.AggregateQuery{
				Query: metric.Query{
					MetricName: MetricPingLatency,
					Start:      start,
					End:        end,
					Order:      metric.OrderAsc,
				},
				Aggregation:    metric.AggAvg,
				Interval:       time.Hour,
				PreserveSeries: true,
			}

			filesBefore, err := store.SQLiteFiles(ctx)
			if err != nil {
				t.Fatalf("read SQLite files before dashboard queries: %v", err)
			}
			runtime.GC()
			var memoryBefore runtime.MemStats
			runtime.ReadMemStats(&memoryBefore)
			started := time.Now()
			for iteration := 0; iteration < 5; iteration++ {
				if _, _, err := GetTrafficRecordsByClientsAndTime(ctx, clientIDs, start, end); err != nil {
					t.Fatalf("batch traffic query: %v", err)
				}
				if _, err := store.DashboardSeries(ctx, query, end); err != nil {
					t.Fatalf("batch latency query: %v", err)
				}
			}
			elapsed := time.Since(started)
			runtime.GC()
			var memoryAfter runtime.MemStats
			runtime.ReadMemStats(&memoryAfter)
			filesAfter, err := store.SQLiteFiles(ctx)
			if err != nil {
				t.Fatalf("read SQLite files after dashboard queries: %v", err)
			}

			var heapGrowth uint64
			if memoryAfter.HeapAlloc > memoryBefore.HeapAlloc {
				heapGrowth = memoryAfter.HeapAlloc - memoryBefore.HeapAlloc
			}
			if heapGrowth > test.maxHeapGrowth {
				t.Fatalf("heap growth = %d bytes, limit = %d", heapGrowth, test.maxHeapGrowth)
			}
			if filesAfter != filesBefore {
				t.Fatalf("dashboard reads changed SQLite files: before=%+v after=%+v", filesBefore, filesAfter)
			}
			t.Logf("nodes=%d five-refresh elapsed=%s heap-growth=%d db-growth=%d", test.nodes, elapsed, heapGrowth, filesAfter.Total()-filesBefore.Total())
		})
	}
}

func metricStore() *metric.Store {
	return store
}

func setMetricStore(next *metric.Store) {
	store = next
}

func openDashboardResourceFixture(t *testing.T, ctx context.Context, nodes int) *metric.Store {
	t.Helper()
	store, err := metric.Open(ctx, metric.SQLiteInDir(
		t.TempDir(),
		metric.WithSQLiteReadPool(1),
		metric.WithSQLiteHeavyReadConcurrency(1),
	))
	if err != nil {
		t.Fatalf("open SQLite dashboard resource fixture: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := createMetricDefinitions(ctx, store); err != nil {
		t.Fatalf("create dashboard resource metrics: %v", err)
	}
	return store
}

func seedDashboardResourceFixture(t *testing.T, ctx context.Context, store *metric.Store, nodes int) ([]string, time.Time, time.Time) {
	t.Helper()
	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-6 * time.Hour)
	clientIDs := make([]string, nodes)
	points := make([]metric.Point, 0, nodes*(4*73+2*361))
	for node := 0; node < nodes; node++ {
		clientID := fmt.Sprintf("node-%03d", node)
		clientIDs[node] = clientID
		baseUp := float64(1_000_000_000 + node*10_000_000)
		baseDown := baseUp * 2
		points = append(points,
			metric.Point{MetricName: MetricNetTotalUp, EntityID: clientID, Timestamp: start.Add(-5 * time.Minute), Value: baseUp},
			metric.Point{MetricName: MetricNetTotalDown, EntityID: clientID, Timestamp: start.Add(-5 * time.Minute), Value: baseDown},
		)
		for sample := 0; sample <= 72; sample++ {
			timestamp := start.Add(time.Duration(sample) * 5 * time.Minute)
			up := float64(2_000_000 + node*1000 + sample)
			down := float64(4_000_000 + node*2000 + sample*2)
			points = append(points,
				metric.Point{MetricName: MetricNetTotalUp, EntityID: clientID, Timestamp: timestamp, Value: baseUp + float64(sample+1)*up},
				metric.Point{MetricName: MetricNetTotalDown, EntityID: clientID, Timestamp: timestamp, Value: baseDown + float64(sample+1)*down},
				metric.Point{MetricName: MetricTrafficUp, EntityID: clientID, Timestamp: timestamp, Value: up},
				metric.Point{MetricName: MetricTrafficDown, EntityID: clientID, Timestamp: timestamp, Value: down},
			)
		}
		for task := 0; task < 2; task++ {
			tags := map[string]string{"task_id": fmt.Sprintf("task-%d", task)}
			for sample := 0; sample <= 360; sample++ {
				value := float64(20 + node + task*5 + sample%17)
				if sample%41 == 0 {
					value = -1
				}
				points = append(points, metric.Point{
					MetricName: MetricPingLatency,
					EntityID:   clientID,
					Timestamp:  start.Add(time.Duration(sample) * time.Minute),
					Value:      value,
					Tags:       tags,
				})
			}
		}
	}
	if err := store.WriteBatch(ctx, points); err != nil {
		t.Fatalf("seed dashboard resource points: %v", err)
	}
	return clientIDs, start, end
}
