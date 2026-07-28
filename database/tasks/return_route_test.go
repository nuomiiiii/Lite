package tasks

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestClassifyReturnRoute(t *testing.T) {
	tests := []struct {
		carrier string
		path    models.StringArray
		want    string
	}{
		{"mobile", models.StringArray{"AS3356", "AS58807", "AS9808"}, "CMIN2"},
		{"mobile", models.StringArray{"AS58453", "AS9808"}, "CMI"},
		{"telecom", models.StringArray{"AS23764", "AS4809"}, "CN2 GIA"},
		{"telecom", models.StringArray{"AS4809", "AS4134"}, "CN2 GT"},
		{"telecom", models.StringArray{"AS4134"}, "163"},
		{"unicom", models.StringArray{"AS9929", "AS4837"}, "9929"},
		{"unicom", models.StringArray{"AS4837"}, "4837"},
	}
	for _, test := range tests {
		got, confidence := classifyReturnRoute(test.carrier, test.path)
		if got != test.want || confidence <= 0 {
			t.Fatalf("classifyReturnRoute(%q, %v) = %q, %.2f; want %q", test.carrier, test.path, got, confidence, test.want)
		}
	}
}

func TestReturnRouteStateRequiresSwitchAndRecoveryConfirmation(t *testing.T) {
	task := models.ReturnRouteTask{Id: 1, Client: "node", ExpectedLine: "CMIN2", SwitchConfirm: 2, RecoveryConfirm: 3}
	status := models.ReturnRouteStatus{TaskId: 1, Confidence: 0.98, ASNPath: models.StringArray{"AS58807"}}
	now := time.Now().UTC()
	if event := advanceReturnRouteState(&status, task, "CMIN2", now); event != nil || status.State != "healthy" {
		t.Fatalf("first expected route should establish a healthy baseline: %#v", status)
	}
	if event := advanceReturnRouteState(&status, task, "CMI", now.Add(time.Minute)); event != nil || status.State != "healthy" || status.CandidateCount != 1 {
		t.Fatalf("one mismatch must not switch: %#v", status)
	}
	event := advanceReturnRouteState(&status, task, "CMI", now.Add(2*time.Minute))
	if event == nil || event.Kind != "switch" || status.State != "switched" || status.CurrentLine != "CMI" {
		t.Fatalf("second mismatch should switch: event=%#v status=%#v", event, status)
	}
	for i := 1; i < 3; i++ {
		if event := advanceReturnRouteState(&status, task, "CMIN2", now.Add(time.Duration(2+i)*time.Minute)); event != nil {
			t.Fatalf("recovery fired before third confirmation: %#v", event)
		}
	}
	event = advanceReturnRouteState(&status, task, "CMIN2", now.Add(5*time.Minute))
	if event == nil || event.Kind != "recovery" || status.State != "healthy" {
		t.Fatalf("third expected route should recover: event=%#v status=%#v", event, status)
	}
}
