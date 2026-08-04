package notifier

import (
	"testing"

	"github.com/komari-monitor/komari/database/models"
)

func TestCapacityMetricsUseClientTotals(t *testing.T) {
	client := &models.Client{MemTotal: 200, SwapTotal: 400, DiskTotal: 800}
	record := models.Record{
		Ram: 100, RamTotal: 0,
		Swap: 100, SwapTotal: 0,
		Disk: 200, DiskTotal: 0,
	}
	tests := []struct {
		metric string
		want   float32
	}{
		{metric: "ram", want: 50},
		{metric: "swap", want: 25},
		{metric: "disk", want: 25},
	}
	for _, test := range tests {
		if got := getMetricValue(record, test.metric, client); got != test.want {
			t.Fatalf("%s usage = %v, want %v", test.metric, got, test.want)
		}
		if !metricNeedsClientCapacity(test.metric) {
			t.Fatalf("%s should require client capacity", test.metric)
		}
	}
}

func TestCapacityMetricRejectsMissingOrZeroClientTotal(t *testing.T) {
	record := models.Record{Ram: 100}
	if got := getMetricValue(record, "ram", nil); got != 0 {
		t.Fatalf("RAM usage without client = %v, want 0", got)
	}
	if got := getMetricValue(record, "ram", &models.Client{}); got != 0 {
		t.Fatalf("RAM usage with zero capacity = %v, want 0", got)
	}
}

func TestCheckMetricThresholdUsesLoadedCapacity(t *testing.T) {
	records := []models.Record{{Ram: 60}, {Ram: 80}, {Ram: 10}}
	task := models.LoadNotification{Metric: "ram", Threshold: 50, Ratio: 0.5}
	client := &models.Client{MemTotal: 100}
	if !checkMetricThreshold(records, task, client) {
		t.Fatal("two of three RAM samples should exceed the threshold")
	}
	if checkMetricThreshold(records, task, nil) {
		t.Fatal("capacity metric without client data should not trigger")
	}
}
