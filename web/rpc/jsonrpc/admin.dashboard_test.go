package jsonrpc

import (
	"context"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/trafficledger"
	"github.com/komari-monitor/komari/pkg/metric"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
