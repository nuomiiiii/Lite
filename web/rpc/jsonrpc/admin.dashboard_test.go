package jsonrpc

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/trafficledger"
)

func TestSummarizeDashboardTrafficUsesPerClientBillingRules(t *testing.T) {
	now := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	clients := []models.Client{
		{UUID: "a", TrafficLimitType: "sum"},
		{UUID: "b", TrafficLimitType: "max"},
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
	}, now)

	if !summary.HistoryReady {
		t.Fatal("complete dashboard history reported as incomplete")
	}
	if summary.TodayUp != 120 || summary.TodayDown != 120 || summary.TodayBillable != 220 {
		t.Fatalf("unexpected today totals: %#v", summary)
	}
	if got := summary.Daily[0].Billable; got != 60 {
		t.Fatalf("historical billable = %d, want 60", got)
	}
	if len(summary.Daily) != trafficledger.DashboardHistoryDays {
		t.Fatalf("daily points = %d, want %d", len(summary.Daily), trafficledger.DashboardHistoryDays)
	}
}

func TestSummarizeDashboardTrafficMarksMissingHistory(t *testing.T) {
	summary := summarizeDashboardTraffic(
		[]models.Client{{UUID: "a"}},
		nil,
		map[string]trafficledger.Usage{"a": {}},
		time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC),
	)
	if summary.HistoryReady {
		t.Fatal("missing ledger rows reported as ready")
	}
}
