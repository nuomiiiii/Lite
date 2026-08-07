package tasks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestClassifyReturnRoute(t *testing.T) {
	tests := []struct {
		path models.StringArray
		want string
	}{
		{models.StringArray{"AS3356", "AS58807", "AS9808", "AS56041"}, "CMIN2"},
		{models.StringArray{"AS58453", "AS9808"}, "CMI"},
		{models.StringArray{"AS23764", "AS4809"}, "CN2 GIA"},
		{models.StringArray{"AS4809", "AS4134"}, returnRouteLineCN2Pending},
		{models.StringArray{"AS10099", "AS9929"}, returnRouteLineCUGVIP},
		{models.StringArray{"AS9929", "AS10099"}, returnRouteLineCUGVIP},
		{models.StringArray{"AS10099", "AS4837"}, returnRouteLineCUGOptimized},
		{models.StringArray{"AS4837", "AS10099"}, returnRouteLineCUGOptimized},
		{models.StringArray{"AS10099"}, returnRouteLineCUGVIP},
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
		returnRouteLineCUGVIP: true, returnRouteLineCUGOptimized: true, "9929": true, "4837": true,
	}
	for _, line := range returnRouteLines() {
		delete(want, line)
	}
	if len(want) != 0 {
		t.Fatalf("return route options are missing cross-carrier lines: %v", want)
	}
	lines := returnRouteLines()
	if indexOfReturnRouteLine(lines, returnRouteLineCUGVIP) >= indexOfReturnRouteLine(lines, returnRouteLineCUGOptimized) ||
		indexOfReturnRouteLine(lines, returnRouteLineCUGOptimized) >= indexOfReturnRouteLine(lines, "9929") ||
		indexOfReturnRouteLine(lines, "9929") >= indexOfReturnRouteLine(lines, "4837") {
		t.Fatalf("Unicom lines are not in CUG VIP, CUG 优化, 9929, 4837 order: %v", lines)
	}
	if indexOfReturnRouteLine(lines, "10099") != len(lines) {
		t.Fatalf("legacy 10099 must not be exposed as a selectable line: %v", lines)
	}
}

func TestNormalizeLegacy10099ExpectedLine(t *testing.T) {
	if got := normalizeReturnRouteLine(" 10099 "); got != returnRouteLineCUGVIP {
		t.Fatalf("normalizeReturnRouteLine(10099) = %q; want %q", got, returnRouteLineCUGVIP)
	}
}

func indexOfReturnRouteLine(lines []string, wanted string) int {
	for index, line := range lines {
		if line == wanted {
			return index
		}
	}
	return len(lines)
}

func TestClassifyReturnRouteUsesLocalCN2PrefixWhenCymruMisses(t *testing.T) {
	ips := []string{"207.57.144.1", "218.30.48.97", "59.43.159.17", "61.175.22.42"}
	asns := map[string]int{
		"207.57.144.1": 1054,
		"218.30.48.97": 4134,
		"61.175.22.42": 4134,
	}
	if line, confidence := classifyReturnRouteHops(ips, asns); line != returnRouteLineCN2Pending || confidence <= 0 {
		t.Fatalf("incomplete local 59.43 feature classified as %q, %.2f; want %s", line, confidence, returnRouteLineCN2Pending)
	}

	gias := map[string]int{"207.57.144.1": 23764}
	if line, confidence := classifyReturnRouteHops([]string{"207.57.144.1", "59.43.159.17"}, gias); line != "CN2 GIA" || confidence < 0.9 {
		t.Fatalf("AS23764 -> 59.43 classified as %q, %.2f; want CN2 GIA", line, confidence)
	}
}

func TestClassifyCN2UsesOrderedCarrierSegments(t *testing.T) {
	tests := []struct {
		name string
		hops []returnRouteSignature
		want string
	}{
		{
			name: "foreign AS4134 handoff followed by continuous CN2 is GIA",
			hops: []returnRouteSignature{
				{ip: "218.30.48.21", asn: 4134},
				{ip: "59.43.182.186"},
				{ip: "59.43.38.165"},
				{ip: "59.43.138.49"},
				{ip: "59.43.80.141"},
			},
			want: "CN2 GIA",
		},
		{
			name: "continuous CN2 followed by one final AS4134 access hop is GIA",
			hops: []returnRouteSignature{
				{ip: "207.57.144.1", asn: 1054},
				{ip: "107.155.0.34", asn: 21859},
				{ip: "218.30.48.97", asn: 4134},
				{ip: "59.43.246.237", asn: 4809},
				{ip: "59.43.39.85", asn: 4809},
				{ip: "59.43.159.17", asn: 4809},
				{ip: "59.43.80.141", asn: 4809},
				{hidden: true},
				{ip: "60.188.65.38", asn: 4134},
				{hidden: true},
			},
			want: "CN2 GIA",
		},
		{
			name: "one CN2 hop followed by final AS4134 access remains pending",
			hops: []returnRouteSignature{
				{ip: "218.30.48.97", asn: 4134},
				{ip: "59.43.159.17", asn: 4809},
				{ip: "60.188.65.38", asn: 4134},
			},
			want: returnRouteLineCN2Pending,
		},
		{
			name: "global CN2 ingress is GIA",
			hops: []returnRouteSignature{{asn: 23764}, {asn: 4809}},
			want: "CN2 GIA",
		},
		{
			name: "one near-terminal 163 hop is pending",
			hops: []returnRouteSignature{
				{ip: "218.30.48.21", asn: 4134},
				{ip: "59.43.182.186"},
				{ip: "202.97.12.1", asn: 4134},
				{ip: "61.175.22.42", asn: 4134},
			},
			want: returnRouteLineCN2Pending,
		},
		{
			name: "multiple domestic 163 transit hops are GT",
			hops: []returnRouteSignature{
				{ip: "59.43.182.186"},
				{ip: "202.97.12.1", asn: 4134},
				{ip: "202.97.12.2", asn: 4134},
				{ip: "61.175.22.42", asn: 4134},
			},
			want: "CN2 GT",
		},
		{
			name: "hidden hops do not mask a visible 163 transit segment",
			hops: []returnRouteSignature{
				{hidden: true},
				{ip: "100.72.56.6"},
				{hidden: true},
				{ip: "193.41.250.173"},
				{ip: "193.41.250.146"},
				{ip: "218.30.48.142", asn: 4134},
				{ip: "202.97.33.125", asn: 4134},
				{ip: "59.43.182.89", asn: 4809},
				{ip: "202.97.100.222", asn: 4134},
				{hidden: true},
				{hidden: true},
				{ip: "202.97.101.238", asn: 4134},
				{ip: "60.191.202.142", asn: 4134},
				{hidden: true},
				{hidden: true},
				{ip: "183.131.147.4", asn: 4134},
			},
			want: "CN2 GT",
		},
		{
			name: "one terminal 202.97 handoff followed by local access is GIA",
			hops: []returnRouteSignature{
				{hidden: true},
				{ip: "10.110.193.1"},
				{ip: "218.30.48.73", asn: 4134},
				{ip: "59.43.181.145", asn: 4809},
				{ip: "59.43.38.185", asn: 4809},
				{ip: "59.43.138.57", asn: 4809},
				{hidden: true},
				{ip: "202.97.23.230", asn: 4134},
				{ip: "60.191.202.154", asn: 4134},
				{hidden: true},
				{hidden: true},
				{ip: "183.131.147.4", asn: 4134},
			},
			want: "CN2 GIA",
		},
		{
			name: "one terminal 202.97 handoff without visible access remains pending",
			hops: []returnRouteSignature{
				{ip: "218.30.48.73", asn: 4134},
				{ip: "59.43.181.145", asn: 4809},
				{ip: "59.43.38.185", asn: 4809},
				{ip: "59.43.138.57", asn: 4809},
				{ip: "59.43.80.145", asn: 4809},
				{ip: "202.97.23.230", asn: 4134},
			},
			want: returnRouteLineCN2Pending,
		},
		{
			name: "one terminal 202.97 handoff and one visible access hop is GIA",
			hops: []returnRouteSignature{
				{hidden: true},
				{ip: "10.110.193.1"},
				{ip: "218.30.48.73", asn: 4134},
				{ip: "59.43.181.145", asn: 4809},
				{ip: "59.43.38.185", asn: 4809},
				{ip: "59.43.138.57", asn: 4809},
				{ip: "59.43.80.145", asn: 4809},
				{ip: "202.97.23.230", asn: 4134},
				{ip: "60.191.202.154", asn: 4134},
				{hidden: true},
				{hidden: true},
			},
			want: "CN2 GIA",
		},
		{
			name: "CN2 prefix works without ASN lookup",
			hops: []returnRouteSignature{{ip: "59.43.182.186"}, {ip: "59.43.38.165"}},
			want: "CN2 GIA",
		},
		{
			name: "hidden hops do not mask a visible continuous CN2 path",
			hops: []returnRouteSignature{
				{ip: "59.43.182.186"}, {hidden: true}, {hidden: true}, {hidden: true}, {ip: "59.43.38.165"},
			},
			want: "CN2 GIA",
		},
		{
			name: "hidden hops keep an incomplete CN2 path pending",
			hops: []returnRouteSignature{
				{ip: "59.43.182.186"}, {hidden: true}, {hidden: true}, {hidden: true}, {ip: "61.175.22.42", asn: 4134},
			},
			want: returnRouteLineCN2Pending,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line, confidence := classifyReturnRouteSignatures(test.hops)
			if line != test.want || confidence <= 0 {
				t.Fatalf("classified as %q, %.2f; want %q", line, confidence, test.want)
			}
		})
	}
}

func TestCN2ManualAndBGPPrefixRulesStaySeparated(t *testing.T) {
	base, err := compileReturnRouteRules(builtinReturnRouteRuleJSON)
	if err != nil {
		t.Fatal(err)
	}
	if base.hasPrefix("telecom_163", "202.97.1.1") || base.hasPrefix("telecom_163", "202.97.200.1") {
		t.Fatal("manual rules still contain the broad 202.97.0.0/16 prefix")
	}

	data, err := os.ReadFile("return_route_bgp_prefixes.json")
	if err != nil {
		t.Fatal(err)
	}
	bgp, err := compileReturnRouteBGPRules(data)
	if err != nil {
		t.Fatal(err)
	}
	rules := mergeReturnRouteRules(base, bgp)
	if !rules.hasPrefix("telecom_163", "202.97.1.1") {
		t.Fatal("BGP rules no longer recognize 202.97.0.0/17")
	}
	if !rules.hasPrefix("unicom_4837", "202.97.200.1") {
		t.Fatal("BGP rules no longer recognize 202.97.128.0/17 in its generated group")
	}
}

func TestFailedBGPRefreshPreservesActiveRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "temporary failure", http.StatusBadGateway)
	}))
	defer server.Close()
	t.Setenv("KOMARI_RETURN_ROUTE_BGP_RULE_URL", server.URL)

	originalClient := returnRouteBGPHTTPClient
	returnRouteBGPHTTPClient = server.Client()
	t.Cleanup(func() { returnRouteBGPHTTPClient = originalClient })

	before := currentReturnRouteRules()
	if err := refreshReturnRouteBGPRules(context.Background()); err == nil {
		t.Fatal("failed BGP response unexpectedly succeeded")
	}
	if after := currentReturnRouteRules(); after != before {
		t.Fatal("failed BGP refresh replaced the last active rule set")
	}
}

func TestRemote9929PrefixOverridesConflictingASN(t *testing.T) {
	data, err := os.ReadFile("return_route_bgp_prefixes.json")
	if err != nil {
		t.Fatal(err)
	}
	bgp, err := compileReturnRouteBGPRules(data)
	if err != nil {
		t.Fatal(err)
	}
	rules := mergeReturnRouteRules(currentReturnRouteRules(), bgp)
	hops := []returnRouteSignature{{ip: "210.14.169.190", asn: 4837}}
	if line, confidence := classifyReturnRouteSignaturesWithRules(hops, rules); line != "9929" || confidence < 0.9 {
		t.Fatalf("remote 210.14.0.0/16 rule classified as %q, %.2f; want 9929", line, confidence)
	}
}

func TestASNProvidersUseOrderedFallback(t *testing.T) {
	calls := make([]string, 0, 3)
	provider := func(name string, asn int, err error) returnRouteASNProvider {
		return func(context.Context, net.IP) (int, error) {
			calls = append(calls, name)
			return asn, err
		}
	}
	got := lookupASNWithProviders(context.Background(), net.ParseIP("162.219.85.173"), []returnRouteASNProvider{
		provider("cymru", 0, errors.New("miss")),
		provider("ripestat", 10099, nil),
		provider("bgpview", 9929, nil),
	})
	if got != 10099 || strings.Join(calls, ",") != "cymru,ripestat" {
		t.Fatalf("ordered fallback = AS%d via %v; want AS10099 via Cymru then RIPEstat", got, calls)
	}
}

func TestReturnRouteCooldownDefaultIsThirtyMinutes(t *testing.T) {
	field, ok := reflect.TypeOf(models.ReturnRouteTask{}).FieldByName("Cooldown")
	if !ok || !strings.Contains(field.Tag.Get("gorm"), "default:1800") {
		t.Fatalf("ReturnRouteTask cooldown tag = %q; want default:1800", field.Tag.Get("gorm"))
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

func TestCUGBackboneChangeCountsAsSwitch(t *testing.T) {
	task := models.ReturnRouteTask{
		Id: 1, Client: "node", Carrier: "unicom", ExpectedLine: returnRouteLineCUGVIP,
		SwitchConfirm: 1, RecoveryConfirm: 1,
	}
	status := models.ReturnRouteStatus{TaskId: 1, CurrentLine: returnRouteLineCUGVIP, State: "healthy"}
	now := time.Now().UTC()

	event := advanceReturnRouteState(&status, task, returnRouteLineCUGOptimized, now)
	if event == nil || event.Kind != "switch" || event.FromLine != returnRouteLineCUGVIP || event.ToLine != returnRouteLineCUGOptimized {
		t.Fatalf("CUG backbone change did not create a switch event: event=%#v status=%#v", event, status)
	}
}

func TestFormatReturnRouteNotificationUsesChineseCarrierAndExpectedLine(t *testing.T) {
	task := models.ReturnRouteTask{
		Name: "VMISS_LAX_CM", Carrier: "telecom", Region: "华东",
		Target: "zj-ningbo-cm-v4.ip.zstaticcdn.com", ExpectedLine: "CN2 GIA",
	}
	event := models.ReturnRouteEvent{
		ExpectedLine: "CN2 GIA", FromLine: "CMIN2", ToLine: "CMI", Confidence: 0.98,
		ASNPath: models.StringArray{"AS1054", "AS58807", "AS9808", "AS56041"},
	}
	want := "任务：VMISS_LAX_CM\n" +
		"运营商/地区：中国电信 / 华东\n" +
		"探测目标：zj-ningbo-cm-v4.ip.zstaticcdn.com\n" +
		"预期线路：CN2 GIA\n" +
		"线路变化：CMIN2 -> CMI\n" +
		"识别置信度：98%\n" +
		"关键 ASN：AS1054 -> AS58807 -> AS9808 -> AS56041"
	if got := formatReturnRouteNotification(task, event); got != want {
		t.Fatalf("notification = %q, want %q", got, want)
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

func TestPendingCN2ObservationPreservesConfirmedState(t *testing.T) {
	now := time.Now().UTC()
	changedAt := now.Add(-time.Hour)
	lastNotifiedAt := now.Add(-2 * time.Hour)
	task := models.ReturnRouteTask{ExpectedLine: "CN2 GIA", Notify: true, Cooldown: 60, SwitchConfirm: 2, RecoveryConfirm: 2}
	status := models.ReturnRouteStatus{
		CurrentLine: "CN2 GT", State: "switched", CandidateLine: "CN2 GIA", CandidateCount: 1,
		LastChangedAt: &changedAt, LastNotifiedAt: &lastNotifiedAt,
	}

	if event := applyReturnRouteObservation(&status, task, returnRouteLineCN2Pending, now); event != nil {
		t.Fatalf("pending observation created an event: %#v", event)
	}
	if status.CurrentLine != "CN2 GT" || status.State != "switched" || status.CandidateLine != returnRouteLineCN2Pending || status.CandidateCount != 0 {
		t.Fatalf("pending observation changed confirmed state: %#v", status)
	}
	if status.LastChangedAt == nil || !status.LastChangedAt.Equal(changedAt) {
		t.Fatalf("pending observation changed last transition time: %#v", status.LastChangedAt)
	}
	if shouldSendReturnRouteRepeatNotificationAfterObservation(task, status, returnRouteLineCN2Pending, now) {
		t.Fatal("pending observation triggered a repeated switch notification")
	}

	empty := models.ReturnRouteStatus{}
	applyReturnRouteObservation(&empty, task, returnRouteLineCN2Pending, now)
	if empty.State != "unknown" || empty.CurrentLine != "" || empty.CandidateCount != 0 {
		t.Fatalf("pending observation established an unconfirmed baseline: %#v", empty)
	}
}

func TestBuildReturnRouteRepeatNotificationOnlyWhileSwitched(t *testing.T) {
	task := models.ReturnRouteTask{
		Id: 7, Client: "node", Name: "route", Carrier: "telecom", Region: "华东",
		Target: "203.0.113.1", IPVersion: 4, ExpectedLine: "CN2 GIA",
	}
	now := time.Now().UTC()
	status := models.ReturnRouteStatus{
		TaskId: task.Id, State: "switched", CurrentLine: "163", Confidence: 0.96,
		ASNPath: models.StringArray{"AS4134"}, RoutePath: models.StringArray{"1 203.0.113.1 2.0ms"},
	}

	reminder := buildReturnRouteRepeatNotification(task, status, now)
	if reminder == nil {
		t.Fatal("switched route should create a repeat notification")
	}
	if reminder.Kind != "switch" || reminder.FromLine != "CN2 GIA" || reminder.ToLine != "163" || !reminder.OccurredAt.Equal(now) {
		t.Fatalf("repeat notification = %#v", reminder)
	}

	status.State = "healthy"
	if reminder := buildReturnRouteRepeatNotification(task, status, now); reminder != nil {
		t.Fatalf("healthy route created a repeat notification: %#v", reminder)
	}
}

func TestReturnRouteNotificationSwitchesAreIndependent(t *testing.T) {
	tests := []struct {
		name           string
		notifySwitch   bool
		notifyRecovery bool
		wantSwitch     bool
		wantRecovery   bool
	}{
		{name: "both enabled", notifySwitch: true, notifyRecovery: true, wantSwitch: true, wantRecovery: true},
		{name: "switch only", notifySwitch: true, notifyRecovery: false, wantSwitch: true, wantRecovery: false},
		{name: "recovery only", notifySwitch: false, notifyRecovery: true, wantSwitch: false, wantRecovery: true},
		{name: "both disabled", notifySwitch: false, notifyRecovery: false, wantSwitch: false, wantRecovery: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task := models.ReturnRouteTask{Notify: test.notifySwitch, NotifyRecovery: test.notifyRecovery}
			if got := shouldSendReturnRouteEventNotification(task, models.ReturnRouteEvent{Kind: "switch"}); got != test.wantSwitch {
				t.Fatalf("switch notification = %v, want %v", got, test.wantSwitch)
			}
			if got := shouldSendReturnRouteEventNotification(task, models.ReturnRouteEvent{Kind: "recovery"}); got != test.wantRecovery {
				t.Fatalf("recovery notification = %v, want %v", got, test.wantRecovery)
			}
		})
	}
}

func TestReturnRouteRepeatNotificationRequiresCooldownAndSwitchToggle(t *testing.T) {
	now := time.Now().UTC()
	last := now.Add(-239 * time.Second)
	task := models.ReturnRouteTask{Notify: true, Cooldown: 240}
	status := models.ReturnRouteStatus{State: "switched", CurrentLine: "CMIN2", LastNotifiedAt: &last}

	if shouldSendReturnRouteRepeatNotification(task, status, now) {
		t.Fatal("repeat notification fired during the cooldown")
	}
	last = now.Add(-240 * time.Second)
	status.LastNotifiedAt = &last
	if !shouldSendReturnRouteRepeatNotification(task, status, now) {
		t.Fatal("repeat notification did not fire after the cooldown")
	}
	task.Notify = false
	if shouldSendReturnRouteRepeatNotification(task, status, now) {
		t.Fatal("repeat notification fired while switch notifications were disabled")
	}
	task.Notify = true
	status.State = "healthy"
	if shouldSendReturnRouteRepeatNotification(task, status, now) {
		t.Fatal("repeat notification fired after the route recovered")
	}
}

func TestQueryReturnRouteTasksFiltersAndPaginates(t *testing.T) {
	db, tasks := seedReturnRouteQueryData(t)

	result, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{Page: 1, PageSize: 1, Keyword: "tokyo", Carrier: "telecom", State: "switched"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Tasks) != 1 || result.Tasks[0].Id != tasks[0].Id {
		t.Fatalf("filtered tasks = %#v, total=%d", result.Tasks, result.Total)
	}
	if len(result.Statuses) != 1 || result.Statuses[0].State != "switched" {
		t.Fatalf("filtered statuses = %#v", result.Statuses)
	}
	exact, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{TaskID: tasks[1].Id})
	if err != nil {
		t.Fatal(err)
	}
	if exact.Total != 1 || len(exact.Tasks) != 1 || exact.Tasks[0].Id != tasks[1].Id {
		t.Fatalf("exact task filter = %#v, total=%d", exact.Tasks, exact.Total)
	}

	page, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{Page: 2, PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Tasks) != 1 || page.Tasks[0].Id != tasks[1].Id {
		t.Fatalf("second page = %#v, total=%d", page.Tasks, page.Total)
	}

	disabled, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{State: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Total != 1 || disabled.Tasks[0].Id != tasks[2].Id {
		t.Fatalf("disabled tasks = %#v", disabled.Tasks)
	}

	utils.StartReturnRouteProbe(tasks[1].Id)
	defer utils.FinishReturnRouteProbe(tasks[1].Id)
	probing, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{State: "probing"})
	if err != nil {
		t.Fatal(err)
	}
	if probing.Total != 1 || len(probing.Tasks) != 1 || probing.Tasks[0].Id != tasks[1].Id || len(probing.ProbingTaskIDs) != 1 || probing.ProbingTaskIDs[0] != tasks[1].Id {
		t.Fatalf("probing tasks = %#v, probing ids=%v, total=%d", probing.Tasks, probing.ProbingTaskIDs, probing.Total)
	}
	healthyWhileProbing, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{State: "healthy"})
	if err != nil {
		t.Fatal(err)
	}
	if healthyWhileProbing.Total != 0 {
		t.Fatalf("healthy filter included an in-flight probe: %#v", healthyWhileProbing.Tasks)
	}
}

func TestGetReturnRouteSummary(t *testing.T) {
	db, tasks := seedReturnRouteQueryData(t)
	now := time.Now().UTC().Truncate(time.Second)
	events := []models.ReturnRouteEvent{
		{TaskId: tasks[0].Id, Client: tasks[0].Client, Kind: "switch", FromLine: "CN2 GIA", ToLine: "CMIN2", Confidence: 0.98, OccurredAt: now.Add(-time.Hour)},
		{TaskId: tasks[1].Id, Client: tasks[1].Client, Kind: "recovery", FromLine: "4837", ToLine: "9929", Confidence: 0.96, OccurredAt: now.Add(-25 * time.Hour)},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE return_route_events SET carrier = NULL, region = NULL, expected_line = NULL WHERE id = ?", events[1].Id).Error; err != nil {
		t.Fatal(err)
	}

	result, err := getReturnRouteSummary(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tasks != 3 || result.Active != 2 || result.Healthy != 1 || result.Switched != 1 || result.Abnormal != 0 || result.RecentEvents != 1 {
		t.Fatalf("summary = %#v", result)
	}
	if err := db.Model(&models.ReturnRouteStatus{}).Where("task_id = ?", tasks[0].Id).Update("last_error", "probe timeout").Error; err != nil {
		t.Fatal(err)
	}
	result, err = getReturnRouteSummary(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Healthy != 1 || result.Switched != 0 || result.Abnormal != 1 {
		t.Fatalf("summary after probe failure = %#v", result)
	}
}

func TestQueryReturnRouteEventsFiltersSnapshotsAndLegacyRows(t *testing.T) {
	db, tasks := seedReturnRouteQueryData(t)
	now := time.Now().UTC().Truncate(time.Second)
	events := []models.ReturnRouteEvent{
		{
			TaskId: tasks[0].Id, Client: tasks[0].Client, TaskName: tasks[0].Name, Carrier: tasks[0].Carrier,
			Region: tasks[0].Region, Target: tasks[0].Target, IPVersion: 4, ExpectedLine: "CN2 GIA",
			Kind: "switch", FromLine: "CN2 GIA", ToLine: "CMIN2", Confidence: 0.98,
			ASNPath: models.StringArray{"AS58807", "AS9808"}, RoutePath: models.StringArray{"1 1.1.1.1 2.0ms", "2 223.5.5.5 8.0ms"}, OccurredAt: now,
		},
		{
			TaskId: tasks[1].Id, Client: tasks[1].Client, Kind: "switch", FromLine: "9929", ToLine: "4837",
			Confidence: 0.96, ASNPath: models.StringArray{"AS4837"}, OccurredAt: now.Add(-time.Hour),
		},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE return_route_events SET carrier = NULL, region = NULL, expected_line = NULL WHERE id = ?", events[1].Id).Error; err != nil {
		t.Fatal(err)
	}

	result, err := queryReturnRouteEvents(db, ReturnRouteEventQuery{
		Page: 1, PageSize: 20, Keyword: "AS58807", ExpectedLine: "CN2 GIA", ActualLine: "CMIN2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Events) != 1 || result.Events[0].TaskName != tasks[0].Name || result.Events[0].NodeName != "Tokyo-01" {
		t.Fatalf("snapshot event query = %#v, total=%d", result.Events, result.Total)
	}
	if len(result.Events[0].RoutePath) != 2 {
		t.Fatalf("snapshot route path = %#v", result.Events[0].RoutePath)
	}

	legacy, err := queryReturnRouteEvents(db, ReturnRouteEventQuery{Keyword: "210.13.64.1", Carrier: "unicom", ExpectedLine: "9929", Region: "华东"})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Total != 1 || len(legacy.Events) != 1 {
		t.Fatalf("legacy event query = %#v, total=%d", legacy.Events, legacy.Total)
	}
	if legacy.Events[0].ExpectedLine != "9929" || legacy.Events[0].Target != "210.13.64.1" || legacy.Events[0].TaskName != tasks[1].Name {
		t.Fatalf("legacy event fallback = %#v", legacy.Events[0])
	}
}

func seedReturnRouteQueryData(t *testing.T) (*gorm.DB, []models.ReturnRouteTask) {
	t.Helper()
	dsn := fmt.Sprintf("file:return-route-query-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ReturnRouteTask{}, &models.ReturnRouteStatus{}, &models.ReturnRouteEvent{}); err != nil {
		t.Fatal(err)
	}
	clients := []models.Client{
		{UUID: "node-a", Token: "token-a", Name: "Tokyo-01"},
		{UUID: "node-b", Token: "token-b", Name: "Singapore-02"},
		{UUID: "node-c", Token: "token-c", Name: "HongKong-03"},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	tasks := []models.ReturnRouteTask{
		{Name: "Tokyo Telecom", Client: "node-a", Carrier: "telecom", Region: "华东", Target: "223.5.5.5", IPVersion: 4, ExpectedLine: "CN2 GIA", Protocol: "icmp", Interval: 180, SwitchConfirm: 2, RecoveryConfirm: 3, Enabled: true},
		{Name: "Singapore Unicom", Client: "node-b", Carrier: "unicom", Region: "华东", Target: "210.13.64.1", IPVersion: 4, ExpectedLine: "9929", Protocol: "icmp", Interval: 180, SwitchConfirm: 2, RecoveryConfirm: 3, Enabled: true},
		{Name: "Hong Kong Mobile", Client: "node-c", Carrier: "mobile", Region: "华南", Target: "120.232.0.1", IPVersion: 4, ExpectedLine: "CMIN2", Protocol: "icmp", Interval: 180, SwitchConfirm: 2, RecoveryConfirm: 3, Enabled: false},
	}
	if err := db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ReturnRouteTask{}).Where("id = ?", tasks[2].Id).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	tasks[2].Enabled = false
	statuses := []models.ReturnRouteStatus{
		{TaskId: tasks[0].Id, CurrentLine: "CMIN2", State: "switched"},
		{TaskId: tasks[1].Id, CurrentLine: "9929", State: "healthy"},
	}
	if err := db.Create(&statuses).Error; err != nil {
		t.Fatal(err)
	}
	return db, tasks
}
