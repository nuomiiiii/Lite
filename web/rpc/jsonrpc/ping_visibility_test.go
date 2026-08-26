package jsonrpc

import (
	"reflect"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/database/models"
)

func TestFilterPingRecordsByCurrentAssignments(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	taskMap := pingTasksByStringID([]models.PingTask{
		{Id: 1, Clients: models.StringArray{"node-a", "node-c"}},
		{Id: 2, Clients: models.StringArray{"node-b"}},
	})
	records := []models.PingRecord{
		{Client: "node-a", TaskId: 1, Time: now, Value: 10},
		{Client: "node-b", TaskId: 1, Time: now, Value: 20},
		{Client: "node-c", TaskId: 1, Time: now, Value: 30},
		{Client: "node-b", TaskId: 2, Time: now, Value: 40},
		{Client: "node-a", TaskId: 99, Time: now, Value: 50},
	}

	got := filterPingRecordsByCurrentAssignments(records, taskMap)
	want := []models.PingRecord{records[0], records[2], records[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered records = %#v, want %#v", got, want)
	}
}

func TestPingMetricSeriesMatchesCurrentAssignment(t *testing.T) {
	taskMap := pingTasksByStringID([]models.PingTask{
		{Id: 1, Clients: models.StringArray{"node-a"}},
		{Id: 2, Clients: models.StringArray{"node-b"}},
	})
	tests := []struct {
		name   string
		series publicMetricSeries
		want   bool
	}{
		{name: "assigned ping", series: publicMetricSeries{MetricKey: metricstore.MetricPingLatency, EntityID: "node-a", Tags: map[string]string{"task_id": "1"}}, want: true},
		{name: "removed ping", series: publicMetricSeries{MetricKey: metricstore.MetricPingLatency, EntityID: "node-a", Tags: map[string]string{"task_id": "2"}}},
		{name: "deleted task", series: publicMetricSeries{MetricKey: metricstore.MetricPingLoss, EntityID: "node-a", Tags: map[string]string{"task_id": "99"}}},
		{name: "missing task tag", series: publicMetricSeries{MetricKey: metricstore.MetricPingLatency, EntityID: "node-a"}},
		{name: "non ping", series: publicMetricSeries{MetricKey: "cpu.usage", EntityID: "node-a"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pingMetricSeriesMatchesCurrentAssignment(test.series, taskMap); got != test.want {
				t.Fatalf("visibility = %v, want %v", got, test.want)
			}
		})
	}
}
