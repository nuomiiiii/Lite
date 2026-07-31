package jsonrpc

import (
	"context"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/trafficledger"
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
	}, now)

	if !summary.HistoryReady {
		t.Fatal("complete dashboard history reported as incomplete")
	}
	if summary.TodayUp != 120 || summary.TodayDown != 120 || summary.TodayBillable != 140 {
		t.Fatalf("unexpected today totals: %#v", summary)
	}
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
		time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC),
	)
	if summary.HistoryReady {
		t.Fatal("missing ledger rows reported as ready")
	}
}
