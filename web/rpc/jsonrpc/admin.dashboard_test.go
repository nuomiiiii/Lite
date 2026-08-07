package jsonrpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/trafficledger"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/pkg/metric"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDashboardModuleCacheCoalescesConcurrentLoads(t *testing.T) {
	var cache dashboardModuleCache[int]
	var calls atomic.Int32
	now := time.Now().UTC()
	var wait sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := cache.get(context.Background(), now, "same", time.Minute, func() (int, error) {
				calls.Add(1)
				time.Sleep(10 * time.Millisecond)
				return 42, nil
			})
			if err != nil {
				t.Errorf("load cached dashboard module: %v", err)
				return
			}
			results <- value
		}()
	}
	wait.Wait()
	close(results)
	for result := range results {
		assert.Equal(t, 42, result)
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestDashboardModuleCacheHonorsFifteenSecondRefresh(t *testing.T) {
	var cache dashboardModuleCache[int]
	var calls atomic.Int32
	now := time.Now().UTC()
	load := func() (int, error) {
		return int(calls.Add(1)), nil
	}

	first, err := cache.get(context.Background(), now, "same", 15*time.Second, load)
	require.NoError(t, err)
	second, err := cache.get(context.Background(), now.Add(14*time.Second), "same", 15*time.Second, load)
	require.NoError(t, err)
	third, err := cache.get(context.Background(), now.Add(15*time.Second), "same", 15*time.Second, load)
	require.NoError(t, err)

	assert.Equal(t, 1, first)
	assert.Equal(t, 1, second)
	assert.Equal(t, 2, third)
	assert.Equal(t, int32(2), calls.Load())
}

func TestDashboardNavigationFollowsThirdPartyThemeManifest(t *testing.T) {
	t.Chdir(t.TempDir())
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	config.SetDb(db)

	themeDir := filepath.Join("data", "theme", "third-party")
	require.NoError(t, os.MkdirAll(themeDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(themeDir, "komari-theme.json"),
		[]byte(`{"navigation":{"server_detail":"/nodes/{uuid}","ping_task_parameter":"task"}}`),
		0o644,
	))
	require.NoError(t, config.Set(config.ThemeKey, "third-party"))

	const uuid = "node-a"
	const detailURL = "/nodes/node-a"
	charts := decorateDashboardNavigation(dashboardChartsResponse{
		Traffic: dashboardTrafficSummary{Ranking: []dashboardTrafficRankItem{{UUID: uuid}}},
		Latency: dashboardLatencySummary{
			Ranking:       []dashboardLatencyRankItem{{UUID: uuid, TaskID: 5}},
			JitterRanking: []dashboardLatencyJitterRankItem{{UUID: uuid, TaskID: 6}},
		},
		PacketLoss: dashboardPacketLossSummary{Ranking: []dashboardPacketLossRankItem{{UUID: uuid, TaskID: 7}}},
	})
	assert.Equal(t, detailURL, charts.Traffic.Ranking[0].DetailURL)
	assert.Equal(t, detailURL+"?task=5", charts.Latency.Ranking[0].DetailURL)
	assert.Equal(t, detailURL+"?task=6", charts.Latency.JitterRanking[0].DetailURL)
	assert.Equal(t, detailURL+"?task=7", charts.PacketLoss.Ranking[0].DetailURL)

	summary := decorateDashboardSummaryNavigation(dashboardResponse{Resources: dashboardResourceSummary{
		CPU:    []dashboardResourceRankItem{{UUID: uuid}},
		Memory: []dashboardResourceRankItem{{UUID: uuid}},
		Disk:   []dashboardResourceRankItem{{UUID: uuid}},
	}})
	assert.Equal(t, detailURL, summary.Resources.CPU[0].DetailURL)
	assert.Equal(t, detailURL, summary.Resources.Memory[0].DetailURL)
	assert.Equal(t, detailURL, summary.Resources.Disk[0].DetailURL)

	// Themes published before navigation metadata existed keep Komari's
	// traditional UUID detail route and do not receive an unknown task query.
	require.NoError(t, os.WriteFile(
		filepath.Join(themeDir, "komari-theme.json"),
		[]byte(`{"name":"Legacy third-party theme"}`),
		0o644,
	))
	legacy := decorateDashboardNavigation(dashboardChartsResponse{
		Latency:    dashboardLatencySummary{Ranking: []dashboardLatencyRankItem{{UUID: uuid, TaskID: 5}}},
		PacketLoss: dashboardPacketLossSummary{Ranking: []dashboardPacketLossRankItem{{UUID: uuid, TaskID: 7}}},
	})
	assert.Equal(t, "/instance/node-a", legacy.Latency.Ranking[0].DetailURL)
	assert.Equal(t, "/instance/node-a", legacy.PacketLoss.Ranking[0].DetailURL)
}

func TestDashboardPreferredPingTaskIDUsesTaskWeightOrder(t *testing.T) {
	tasks := []models.PingTask{
		{Id: 9, Clients: models.StringArray{"node-a"}, Weight: 1},
		{Id: 3, Clients: models.StringArray{"node-a"}, Weight: 2},
	}
	assert.Equal(t, uint(9), dashboardPreferredPingTaskID("node-a", map[uint]struct{}{3: {}, 9: {}}, tasks))
	assert.Zero(t, dashboardPreferredPingTaskID("node-b", map[uint]struct{}{3: {}, 9: {}}, tasks))
}

func TestBuildDashboardStorageUsesNewestCompactionTime(t *testing.T) {
	older := time.Date(2026, 7, 31, 6, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	summary := buildDashboardStorage(context.Background(),
		databaseStorageStatus{
			Location: databaseLocationLocal,
			Files:    &databaseFileSizes{Database: 10, WAL: 2, SHM: 1},
			Runtime:  &databaseRuntimeStatus{LastCycleCompletedAt: &newer},
		},
		databaseStorageStatus{
			Location: databaseLocationLocal,
			Files:    &databaseFileSizes{Database: 20, WAL: 3, SHM: 1},
			Runtime:  &databaseRuntimeStatus{LastCycleCompletedAt: &older},
		},
	)

	if summary.DatabaseFiles != 30 || summary.WAL != 5 || summary.SHM != 2 {
		t.Fatalf("unexpected storage totals: %#v", summary)
	}
	if summary.LastCompactedAt == nil || !summary.LastCompactedAt.Equal(newer) {
		t.Fatalf("last compaction = %v, want %v", summary.LastCompactedAt, newer)
	}
}

func TestDashboardLatencyMinuteAveragesAndJitterRanking(t *testing.T) {
	current := time.Date(2026, 8, 6, 4, 30, 0, 0, time.UTC)
	previous := current.Add(-time.Minute)
	previousAverage, currentAverage, ok := dashboardLatencyMinuteAverages([]metric.AggregatePoint{
		{Bucket: previous, Value: 10, Count: 2},
		{Bucket: previous, Value: 20, Count: 1},
		{Bucket: current, Value: 30, Count: 2},
		{Bucket: current, Value: -1, Count: 5},
	}, previous, current)
	require.True(t, ok)
	assert.InDelta(t, 40.0/3.0, previousAverage, 0.001)
	assert.InDelta(t, 30, currentAverage, 0.001)

	ranking := []dashboardLatencyJitterRankItem{}
	for _, item := range []dashboardLatencyJitterRankItem{
		{Name: "stable", Delta: 0},
		{Name: "improved", Delta: -5},
		{Name: "spike", Delta: 20},
	} {
		ranking = dashboardTopLatencyJitter(ranking, item, 5)
	}
	require.Len(t, ranking, 3)
	assert.Equal(t, []string{"spike", "stable", "improved"}, []string{ranking[0].Name, ranking[1].Name, ranking[2].Name})
}

func TestDashboardLatencyRankingsHonorEveryTopLimit(t *testing.T) {
	for _, limit := range []int{5, 10, 15, 20} {
		var latency []dashboardLatencyRankItem
		var jitter []dashboardLatencyJitterRankItem
		for index := 0; index < 25; index++ {
			latency = dashboardTopLatency(latency, dashboardLatencyRankItem{Name: fmt.Sprintf("node-%02d", index), Average: float64(index)}, limit)
			jitter = dashboardTopLatencyJitter(jitter, dashboardLatencyJitterRankItem{Name: fmt.Sprintf("node-%02d", index), Delta: float64(index)}, limit)
		}
		require.Len(t, latency, limit)
		require.Len(t, jitter, limit)
		assert.Equal(t, float64(24), latency[0].Average)
		assert.Equal(t, float64(24), jitter[0].Delta)
	}
}

func TestSummarizeDashboardPacketLossKeepsWorstOnlineTask(t *testing.T) {
	clients := []models.Client{
		{UUID: "node-a", Name: "Alpha"},
		{UUID: "node-b", Name: "Beta"},
		{UUID: "node-c", Name: "Offline"},
		{UUID: "node-d", Name: "Clean"},
	}
	tasks := []models.PingTask{
		{Id: 1, Name: "A good", Clients: models.StringArray{"node-a"}, Interval: 60},
		{Id: 2, Name: "A worst", Clients: models.StringArray{"node-a"}, Interval: 60},
		{Id: 3, Name: "B new task", Clients: models.StringArray{"node-b"}, Interval: 60},
		{Id: 4, Name: "B enough", Clients: models.StringArray{"node-b"}, Interval: 60},
		{Id: 5, Name: "C lost", Clients: models.StringArray{"node-c"}, Interval: 60},
		{Id: 6, Name: "D clean", Clients: models.StringArray{"node-d"}, Interval: 60},
	}
	points := []metric.AggregatePoint{
		{EntityID: "node-a", Tags: map[string]string{"task_id": "1"}, Value: 0.125, Count: 8},
		{EntityID: "node-a", Tags: map[string]string{"task_id": "2"}, Value: 0.5, Count: 8},
		{EntityID: "node-b", Tags: map[string]string{"task_id": "3"}, Value: 3.0 / 7.0, Count: 7},
		{EntityID: "node-b", Tags: map[string]string{"task_id": "4"}, Value: 0.25, Count: 8},
		{EntityID: "node-c", Tags: map[string]string{"task_id": "5"}, Value: 1, Count: 15},
		{EntityID: "node-d", Tags: map[string]string{"task_id": "6"}, Value: 0, Count: 15},
	}
	online := map[string]struct{}{"node-a": {}, "node-b": {}, "node-d": {}}

	ranking := summarizeDashboardPacketLoss(clients, tasks, points, online, 5)
	require.Len(t, ranking, 2)
	assert.Equal(t, []string{"node-a", "node-b"}, []string{ranking[0].UUID, ranking[1].UUID})
	assert.Equal(t, uint(2), ranking[0].TaskID)
	assert.Equal(t, 4, ranking[0].Lost)
	assert.Equal(t, 8, ranking[0].Total)
	assert.InDelta(t, 50, ranking[0].LossRate, 0.001)
	assert.Equal(t, uint(3), ranking[1].TaskID)
	assert.Equal(t, 3, ranking[1].Lost)
	assert.Equal(t, 7, ranking[1].Total)
	assert.InDelta(t, 300.0/7.0, ranking[1].LossRate, 0.001)
}

func TestDashboardPacketLossOrdering(t *testing.T) {
	items := []dashboardPacketLossRankItem{
		{Name: "later", LossRate: 20, Lost: 2, Valid: 8, clientOrder: 2},
		{Name: "more-loss", LossRate: 20, Lost: 3, Valid: 7, clientOrder: 3},
		{Name: "more-valid", LossRate: 20, Lost: 2, Valid: 9, clientOrder: 1},
		{Name: "highest-rate", LossRate: 30, Lost: 1, Valid: 9, clientOrder: 4},
	}
	var ranking []dashboardPacketLossRankItem
	for _, item := range items {
		ranking = dashboardTopPacketLoss(ranking, item, 20)
	}
	assert.Equal(t, []string{"highest-rate", "more-loss", "more-valid", "later"}, []string{
		ranking[0].Name, ranking[1].Name, ranking[2].Name, ranking[3].Name,
	})
}

func TestDashboardPacketLossHonorsEveryTopLimit(t *testing.T) {
	for _, limit := range []int{5, 10, 15, 20} {
		var ranking []dashboardPacketLossRankItem
		for index := 0; index < 25; index++ {
			ranking = dashboardTopPacketLoss(ranking, dashboardPacketLossRankItem{
				Name: "node", LossRate: float64(index + 1), clientOrder: index,
			}, limit)
		}
		require.Len(t, ranking, limit)
		assert.Equal(t, float64(25), ranking[0].LossRate)
	}
}

func TestSummarizeDashboardTrafficExcludesFreeClientsFromBilling(t *testing.T) {
	now := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	clients := []models.Client{
		{UUID: "a", Price: 10, TrafficLimitType: "sum"},
		{UUID: "b", Price: 0, TrafficLimitType: "max"},
	}
	rows := make([]models.TrafficDailyLedger, 0, 2*(trafficledger.DashboardHistoryDays-1))
	today := trafficledger.BeijingDay(now)
	for offset := trafficledger.DashboardHistoryDays - 1; offset > 0; offset-- {
		day := today.AddDate(0, 0, -offset).Format(time.DateOnly)
		rows = append(rows,
			models.TrafficDailyLedger{Client: "a", Day: day, UpBytes: 10, DownBytes: 20},
			models.TrafficDailyLedger{Client: "b", Day: day, UpBytes: 30, DownBytes: 5},
		)
	}

	summary := summarizeDashboardTraffic(clients, rows, map[string]trafficledger.Usage{
		"a": {Up: 100, Down: 40},
		"b": {Up: 20, Down: 80},
	}, map[string][]trafficledger.HourlyUsage{
		"a": {{Hour: trafficledger.BeijingDay(now).Add(2 * time.Hour), Usage: trafficledger.Usage{Up: 100, Down: 40}}},
		"b": {{Hour: trafficledger.BeijingDay(now).Add(3 * time.Hour), Usage: trafficledger.Usage{Up: 20, Down: 80}}},
	}, nil, now, 5)

	if !summary.HistoryReady {
		t.Fatal("complete dashboard history reported as incomplete")
	}
	if summary.TodayUp != 120 || summary.TodayDown != 120 || summary.TodayBillable != 140 {
		t.Fatalf("unexpected today totals: %#v", summary)
	}
	assert.Equal(t, []string{"a", "b"}, []string{summary.Ranking[0].UUID, summary.Ranking[1].UUID})
	assert.Equal(t, int64(140), summary.Ranking[0].Billable)
	assert.Equal(t, int64(80), summary.Ranking[1].Billable)
	if got := summary.Daily[0].Billable; got != 30 {
		t.Fatalf("historical billable = %d, want 30", got)
	}
	if len(summary.Daily) != trafficledger.DashboardHistoryDays {
		t.Fatalf("daily points = %d, want %d", len(summary.Daily), trafficledger.DashboardHistoryDays)
	}
	if len(summary.Hourly) != now.In(trafficledger.BeijingLocation).Hour()+1 {
		t.Fatalf("hourly points = %d", len(summary.Hourly))
	}
	if summary.Hourly[2].Up != 100 || summary.Hourly[2].Down != 40 || summary.Hourly[3].Up != 120 || summary.Hourly[3].Down != 120 {
		t.Fatalf("unexpected cumulative hourly traffic: %#v", summary.Hourly[:4])
	}
}

func TestSummarizeDashboardTrafficMarksMissingHistory(t *testing.T) {
	summary := summarizeDashboardTraffic(
		[]models.Client{{UUID: "a"}},
		nil,
		map[string]trafficledger.Usage{"a": {}},
		nil,
		nil,
		time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC),
		5,
	)
	if summary.HistoryReady {
		t.Fatal("missing ledger rows reported as ready")
	}
}

func TestSummarizeDashboardTrafficUsesCalibratedDailyAndHourlyValues(t *testing.T) {
	now := time.Date(2026, 8, 2, 8, 30, 0, 0, time.UTC)
	today := trafficledger.BeijingDay(now)
	yesterday := today.AddDate(0, 0, -1).Format(time.DateOnly)
	todayKey := today.Format(time.DateOnly)
	summary := summarizeDashboardTraffic(
		[]models.Client{{UUID: "a", Price: 1, TrafficLimitType: "sum"}},
		[]models.TrafficDailyLedger{{Client: "a", Day: yesterday, UpBytes: 100, DownBytes: 200}},
		map[string]trafficledger.Usage{"a": {Up: 10, Down: 20}},
		map[string][]trafficledger.HourlyUsage{
			"a": {{Hour: today.Add(15 * time.Hour), Usage: trafficledger.Usage{Up: 10, Down: 20}}},
		},
		map[string]trafficledger.SignedUsage{
			"a\x00" + yesterday: {Up: 50, Down: -25},
			"a\x00" + todayKey:  {Up: 5, Down: -10},
		},
		now,
		5,
	)

	assert.Equal(t, int64(150), summary.Daily[len(summary.Daily)-2].Up)
	assert.Equal(t, int64(175), summary.Daily[len(summary.Daily)-2].Down)
	assert.Equal(t, int64(15), summary.TodayUp)
	assert.Equal(t, int64(10), summary.TodayDown)
	assert.Equal(t, int64(25), summary.TodayBillable)
	assert.Equal(t, int64(15), summary.Hourly[len(summary.Hourly)-1].Up)
	assert.Equal(t, int64(10), summary.Hourly[len(summary.Hourly)-1].Down)
}
