package tasks

import (
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
)

func TestClassifyReturnRoute(t *testing.T) {
	tests := []struct {
		path    models.StringArray
		want    string
	}{
		{models.StringArray{"AS3356", "AS58807", "AS9808", "AS56041"}, "CMIN2"},
		{models.StringArray{"AS58453", "AS9808"}, "CMI"},
		{models.StringArray{"AS23764", "AS4809"}, "CN2 GIA"},
		{models.StringArray{"AS4809", "AS4134"}, "CN2 GT"},
		{models.StringArray{"AS9929", "AS4134"}, "9929"},
		{models.StringArray{"AS4134"}, "163"},
		{models.StringArray{"AS4837"}, "4837"},
		{models.StringArray{"AS9808", "AS56041"}, "CMNET"},
	}
	for _, test := range tests {
		got, confidence := classifyReturnRoute(test.path)
		if got != test.want || confidence <= 0 {
			t.Fatalf("classifyReturnRoute(%v) = %q, %.2f; want %q", test.path, got, confidence, test.want)
		}
	}
}

func TestReturnRouteLinesAllowCrossCarrierExpectations(t *testing.T) {
	want := map[string]bool{
		"CMIN2": true, "CMI": true, "CMNET": true,
		"CN2 GIA": true, "CN2 GT": true, "163": true,
		"9929": true, "4837": true,
	}
	for _, line := range returnRouteLines() {
		delete(want, line)
	}
	if len(want) != 0 {
		t.Fatalf("return route options are missing cross-carrier lines: %v", want)
	}
}

func TestReturnRouteCrossCarrierInjectionCountsAsSwitch(t *testing.T) {
	task := models.ReturnRouteTask{Id: 1, Client: "node", Carrier: "telecom", ExpectedLine: "CN2 GIA", SwitchConfirm: 2, RecoveryConfirm: 3}
	status := models.ReturnRouteStatus{TaskId: 1, Confidence: 0.98, ASNPath: models.StringArray{"AS58807", "AS9808", "AS56041"}}
	now := time.Now().UTC()

	line, _ := classifyReturnRoute(status.ASNPath)
	if line != "CMIN2" {
		t.Fatalf("cross-carrier path classified as %q, want CMIN2", line)
	}
	if event := advanceReturnRouteState(&status, task, line, now); event != nil || status.State != "observing" || status.CandidateCount != 1 {
		t.Fatalf("first cross-carrier result should start switch confirmation: event=%#v status=%#v", event, status)
	}
	event := advanceReturnRouteState(&status, task, line, now.Add(time.Minute))
	if event == nil || event.Kind != "switch" || event.FromLine != "CN2 GIA" || event.ToLine != "CMIN2" || status.State != "switched" {
		t.Fatalf("confirmed cross-carrier result should switch: event=%#v status=%#v", event, status)
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
