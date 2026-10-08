package client

import (
	"encoding/json"
	"testing"
	"time"

	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func TestPingResultTimeHonorsRecentFinishedAt(t *testing.T) {
	now := time.Date(2026, 9, 18, 11, 4, 0, 0, time.UTC)
	previous := now.Add(-90 * time.Second)

	if got := pingResultTimeAt(time.Time{}, now); !got.Equal(now) {
		t.Fatalf("zero finished_at = %v, want %v", got, now)
	}
	if got := pingResultTimeAt(previous, now); !got.Equal(previous) {
		t.Fatalf("recent finished_at = %v, want %v", got, previous)
	}
	if got := pingResultTimeAt(now.Add(-7*time.Hour), now); !got.Equal(now) {
		t.Fatalf("old finished_at should clamp to now, got %v", got)
	}
	if got := pingResultTimeAt(now.Add(2*time.Minute), now); !got.Equal(now) {
		t.Fatalf("future finished_at should clamp to now, got %v", got)
	}
}

func TestPingResultValueKeepsFractionAndWholeMilliseconds(t *testing.T) {
	var fractional v2.PingResultParams
	if err := json.Unmarshal([]byte(`{"task_id":1,"ping_type":"icmp","value":17.36,"finished_at":"2026-10-09T00:00:00Z"}`), &fractional); err != nil {
		t.Fatal(err)
	}
	if fractional.Value != 17.36 {
		t.Fatalf("fractional value = %v", fractional.Value)
	}
	var whole v2.PingResultParams
	if err := json.Unmarshal([]byte(`{"task_id":1,"ping_type":"icmp","value":17,"finished_at":"2026-10-09T00:00:00Z"}`), &whole); err != nil {
		t.Fatal(err)
	}
	if whole.ValueMS != nil || whole.LatencyMS() != 17 {
		t.Fatalf("legacy whole value = %+v", whole)
	}
	var precise v2.PingResultParams
	if err := json.Unmarshal([]byte(`{"task_id":1,"ping_type":"icmp","value":17,"value_ms":17.36,"finished_at":"2026-10-09T00:00:00Z"}`), &precise); err != nil {
		t.Fatal(err)
	}
	if precise.LatencyMS() != 17.36 {
		t.Fatalf("precise value = %v", precise.LatencyMS())
	}
	var zero v2.PingResultParams
	if err := json.Unmarshal([]byte(`{"task_id":1,"ping_type":"icmp","value":5,"value_ms":0,"finished_at":"2026-10-09T00:00:00Z"}`), &zero); err != nil {
		t.Fatal(err)
	}
	if zero.LatencyMS() != 0 {
		t.Fatalf("zero precise value = %v", zero.LatencyMS())
	}
}
