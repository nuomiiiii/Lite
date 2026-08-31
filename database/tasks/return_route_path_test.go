package tasks

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
)

func publicMainlandHop(ttl, asn int, ip string) mainlandPathHop {
	return mainlandPathHop{TTL: ttl, IP: ip, ASN: asn, Prefix: mainlandIPPrefix(ip), Public: true}
}

func timeoutMainlandHop(ttl int) mainlandPathHop {
	return mainlandPathHop{TTL: ttl, Timeout: true}
}

func giaBaselineStatus() models.ReturnRouteStatus {
	return models.ReturnRouteStatus{
		CurrentLine:            "CN2 GIA",
		State:                  "healthy",
		BaselineLine:           "CN2 GIA",
		BaselineReady:          true,
		BaselineVersion:       1,
		BaselineTerminalTTL:    12,
		BaselineTerminalAnchor: "AS4809 203.208.0.0/24",
		BaselineRouteSignature: "5 AS4134 202.97.10.0/24|8 AS4134 202.97.20.0/24|12 AS4809 203.208.0.0/24",
	}
}

func TestClassifyMainlandTruncationAfterShanghai(t *testing.T) {
	status := giaBaselineStatus()
	path := []mainlandPathHop{
		publicMainlandHop(5, 4134, "202.97.10.8"),
		publicMainlandHop(8, 4134, "202.97.20.8"),
		timeoutMainlandHop(9),
		timeoutMainlandHop(10),
		timeoutMainlandHop(11),
	}
	got := classifyMainlandReachability("", path, "CN2 GIA", "CN2 GIA", status, false, "203.208.1.1")
	if got.Outcome != mainlandOutcomeTruncated {
		t.Fatalf("still-CN2 truncated path = %#v", got)
	}
	if got.LineState != mainlandLineStateStable {
		t.Fatalf("line state = %s", got.LineState)
	}
}

func TestClassifyMainlandIdentifiedLineIsNotAutomaticallyReachable(t *testing.T) {
	status := giaBaselineStatus()
	status.CurrentLine = "CUG VIP"
	status.BaselineLine = "CUG VIP"
	status.BaselineRouteSignature = "5 AS10099 210.14.0.0/24|9 AS10099 210.14.1.0/24|13 AS10099 210.14.8.0/24"
	status.BaselineTerminalTTL = 13
	status.BaselineTerminalAnchor = "AS10099 210.14.8.0/24"
	path := []mainlandPathHop{
		publicMainlandHop(5, 10099, "210.14.0.8"),
		timeoutMainlandHop(6),
		timeoutMainlandHop(7),
		timeoutMainlandHop(8),
	}
	got := classifyMainlandReachability("", path, "CUG VIP", "9929", status, false, "")
	if got.Outcome != mainlandOutcomeTruncated {
		t.Fatalf("identified CUG VIP still truncated, got %#v", got)
	}
}

func TestClassifyMainlandSwitchIsIndeterminate(t *testing.T) {
	status := giaBaselineStatus()
	status.State = "observing"
	status.CandidateLine = "CUG VIP"
	path := []mainlandPathHop{
		publicMainlandHop(5, 10099, "210.14.0.8"),
		publicMainlandHop(9, 10099, "210.14.1.8"),
	}
	got := classifyMainlandReachability("", path, "CUG VIP", "CN2 GIA", status, true, "210.14.8.1")
	if got.Outcome != mainlandOutcomeIndeterminate || got.LineState != mainlandLineStateSwitching {
		t.Fatalf("switch sample = %#v", got)
	}
}

func TestClassifyMainlandRebasingIsIndeterminate(t *testing.T) {
	status := giaBaselineStatus()
	status.State = "switched"
	status.CurrentLine = "CUG VIP"
	path := []mainlandPathHop{
		publicMainlandHop(5, 10099, "210.14.0.8"),
		timeoutMainlandHop(6),
		timeoutMainlandHop(7),
		timeoutMainlandHop(8),
	}
	got := classifyMainlandReachability("", path, "CUG VIP", "CN2 GIA", status, false, "")
	if got.Outcome != mainlandOutcomeIndeterminate || got.LineState != mainlandLineStateRebasing {
		t.Fatalf("rebasing truncated path must not vote, got %#v", got)
	}
}

func TestClassifyMainlandSilentTargetAtHistoricalAnchor(t *testing.T) {
	status := giaBaselineStatus()
	path := []mainlandPathHop{
		publicMainlandHop(5, 4134, "202.97.10.8"),
		publicMainlandHop(8, 4134, "202.97.20.8"),
		publicMainlandHop(12, 4809, "203.208.0.8"),
	}
	got := classifyMainlandReachability("", path, "CN2 GIA", "CN2 GIA", status, false, "203.208.1.1")
	if got.Outcome != mainlandOutcomeReachable {
		t.Fatalf("silent target at historical anchor = %#v", got)
	}
}

func TestClassifyMainlandMiddleTimeoutsStillReachable(t *testing.T) {
	status := giaBaselineStatus()
	path := []mainlandPathHop{
		publicMainlandHop(5, 4134, "202.97.10.8"),
		timeoutMainlandHop(6),
		timeoutMainlandHop(7),
		timeoutMainlandHop(8),
		publicMainlandHop(12, 4809, "203.208.0.8"),
	}
	got := classifyMainlandReachability("", path, "CN2 GIA", "CN2 GIA", status, false, "")
	if got.Outcome != mainlandOutcomeReachable {
		t.Fatalf("middle * hops with later reply = %#v", got)
	}
}

func TestClassifyMainlandTimeoutsWhereBaselineHadNoLaterHop(t *testing.T) {
	status := giaBaselineStatus()
	status.BaselineRouteSignature = "5 AS4134 202.97.10.0/24|12 AS4809 203.208.0.0/24"
	path := []mainlandPathHop{
		publicMainlandHop(5, 4134, "202.97.10.8"),
		timeoutMainlandHop(6),
		timeoutMainlandHop(7),
		timeoutMainlandHop(8),
		publicMainlandHop(12, 4809, "203.208.0.8"),
	}
	got := classifyMainlandReachability("", path, "CN2 GIA", "CN2 GIA", status, false, "")
	if got.Outcome != mainlandOutcomeReachable {
		t.Fatalf("gap timeouts that still reach terminal = %#v", got)
	}
}

func TestClassifyMainlandNoBaselineIsIndeterminate(t *testing.T) {
	status := models.ReturnRouteStatus{CurrentLine: "CN2 GIA", State: "healthy"}
	path := []mainlandPathHop{
		publicMainlandHop(5, 4134, "202.97.10.8"),
		timeoutMainlandHop(6),
		timeoutMainlandHop(7),
		timeoutMainlandHop(8),
	}
	got := classifyMainlandReachability("", path, "CN2 GIA", "CN2 GIA", status, false, "")
	if got.Outcome != mainlandOutcomeIndeterminate {
		t.Fatalf("no baseline = %#v", got)
	}
}

func TestClassifyMainlandAllTimeoutWithoutBaselineIsIndeterminate(t *testing.T) {
	status := models.ReturnRouteStatus{CurrentLine: "CN2 GIA", State: "healthy"}
	got := classifyMainlandReachability("", []mainlandPathHop{timeoutMainlandHop(1), timeoutMainlandHop(2)}, "UNKNOWN", "CN2 GIA", status, false, "")
	if got.Outcome != mainlandOutcomeIndeterminate {
		t.Fatalf("all timeout without baseline = %#v", got)
	}
}

func TestClassifyMainlandAllTimeoutWithBaselineIsTruncated(t *testing.T) {
	status := giaBaselineStatus()
	got := classifyMainlandReachability("", []mainlandPathHop{timeoutMainlandHop(1), timeoutMainlandHop(2)}, "UNKNOWN", "CN2 GIA", status, false, "")
	if got.Outcome != mainlandOutcomeTruncated {
		t.Fatalf("all timeout with baseline = %#v", got)
	}
}

func TestClassifyMainlandTargetReachedIsReachable(t *testing.T) {
	status := giaBaselineStatus()
	path := []mainlandPathHop{
		timeoutMainlandHop(1),
		publicMainlandHop(5, 4134, "202.97.10.8"),
		publicMainlandHop(12, 4809, "203.208.1.1"),
	}
	got := classifyMainlandReachability("", path, "CN2 GIA", "CN2 GIA", status, true, "203.208.1.1")
	if got.Outcome != mainlandOutcomeReachable {
		t.Fatalf("protocol confirmed target = %#v", got)
	}
}

func TestClassifyMainlandAgentErrorIsInvalid(t *testing.T) {
	status := giaBaselineStatus()
	got := classifyMainlandReachability("need CAP_NET_RAW", nil, "UNKNOWN", "CN2 GIA", status, false, "")
	if got.Outcome != mainlandOutcomeInvalid {
		t.Fatalf("agent error = %#v", got)
	}
}

func TestUpdateMainlandBaselineIgnoresTruncatedPath(t *testing.T) {
	status := giaBaselineStatus()
	now := time.Now().UTC()
	class := mainlandProbeClassification{
		Outcome:        mainlandOutcomeTruncated,
		LineState:      mainlandLineStateStable,
		TerminalTTL:    8,
		TerminalAnchor: "AS4134 1.1.1.0/24",
		Signature:      "5 AS4134 1.1.1.0/24",
	}
	updateMainlandBaseline(&status, class, "CN2 GIA", now)
	if status.BaselineTerminalAnchor != "AS4809 203.208.0.0/24" {
		t.Fatalf("truncated path rewrote baseline: %#v", status)
	}
}

func TestUpdateMainlandBaselineWaitsForStableAnchor(t *testing.T) {
	status := models.ReturnRouteStatus{CurrentLine: "CN2 GIA", State: "healthy"}
	now := time.Now().UTC()
	anchor := "AS4134 202.97.20.0/24"
	class := mainlandProbeClassification{
		Outcome:        mainlandOutcomeIndeterminate,
		LineState:      mainlandLineStateStable,
		Comparable:     true,
		TerminalTTL:    8,
		TerminalAnchor: anchor,
		Signature:      "8 AS4134 202.97.20.0/24",
	}
	updateMainlandBaseline(&status, class, "CN2 GIA", now)
	if status.BaselineReady {
		t.Fatal("single path should not become baseline")
	}
	updateMainlandBaseline(&status, class, "CN2 GIA", now)
	if !status.BaselineReady || status.BaselineTerminalAnchor != anchor {
		t.Fatalf("2/3 identical anchors should ready baseline: %#v", status)
	}
}

func TestUpdateMainlandBaselineAfterSwitchRequiresNewLine(t *testing.T) {
	status := giaBaselineStatus()
	status.State = "switched"
	status.CurrentLine = "CUG VIP"
	now := time.Now().UTC()
	truncated := mainlandProbeClassification{
		Outcome: mainlandOutcomeTruncated, LineState: mainlandLineStateRebasing,
		TerminalAnchor: "AS10099 210.14.0.0/24", Signature: "5 AS10099 210.14.0.0/24",
	}
	updateMainlandBaseline(&status, truncated, "CUG VIP", now)
	if status.BaselineLine != "CN2 GIA" || !status.BaselineReady {
		t.Fatalf("truncated new line must not replace baseline: %#v", status)
	}
	reached := mainlandProbeClassification{
		Outcome: mainlandOutcomeIndeterminate, LineState: mainlandLineStateRebasing,
		TargetReached: true, Comparable: true, TerminalTTL: 10,
		TerminalAnchor: "target 210.14.8.1", Signature: "5 AS10099 210.14.0.0/24|10 AS10099 210.14.8.0/24",
	}
	updateMainlandBaseline(&status, reached, "CUG VIP", now)
	if !status.BaselineReady || status.BaselineLine != "CUG VIP" || status.BaselineVersion < 2 {
		t.Fatalf("reachable new line should rebase: %#v", status)
	}
}
