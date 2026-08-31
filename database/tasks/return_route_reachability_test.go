package tasks

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type mainlandNotifyCall struct {
	title string
	body  string
	kind  string
}

func seedMainlandReachabilityDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:mainland-reachability-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Client{},
		&models.ReturnRouteTask{},
		&models.ReturnRouteStatus{},
		&models.ReturnRouteEvent{},
		&models.ReturnRouteProbeSample{},
		&models.ReturnRouteReachabilityStatus{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func captureMainlandNotify(t *testing.T) *[]mainlandNotifyCall {
	t.Helper()
	calls := &[]mainlandNotifyCall{}
	prevOnline := mainlandClientOnline
	prevNotify := sendMainlandReachabilityEvent
	mainlandClientOnline = func(string) bool { return true }
	sendMainlandReachabilityEvent = func(title, body string, event models.ReturnRouteEvent, _ models.Client) error {
		*calls = append(*calls, mainlandNotifyCall{title: title, body: body, kind: event.Kind})
		return nil
	}
	t.Cleanup(func() {
		mainlandClientOnline = prevOnline
		sendMainlandReachabilityEvent = prevNotify
	})
	return calls
}

func createMainlandTask(t *testing.T, db *gorm.DB, name, client, carrier string, ipVersion int, enabled bool) models.ReturnRouteTask {
	t.Helper()
	task := models.ReturnRouteTask{
		Name: name, Client: client, Carrier: carrier, Region: "华东",
		Target: "1.1.1.1", IPVersion: ipVersion, ExpectedLine: expectedLineForCarrier(carrier),
		Protocol: "icmp", Interval: 180, SwitchConfirm: 2, RecoveryConfirm: 3, Cooldown: 1800,
		Notify: true, NotifyRecovery: true, Enabled: true,
		MainlandReachabilityEnabled: enabled, MainlandReachabilityNotify: true, MainlandReachabilityRecoveryNotify: true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	status := models.ReturnRouteStatus{
		TaskId: task.Id, CurrentLine: task.ExpectedLine, State: "healthy",
		BaselineLine: task.ExpectedLine, BaselineReady: true, BaselineVersion: 1,
		BaselineTerminalTTL: 12, BaselineTerminalAnchor: "target 1.1.1.1",
		BaselineRouteSignature: "8 AS4134 202.97.0.0/24|12 AS4809 203.208.0.0/24",
	}
	if err := db.Create(&status).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

func expectedLineForCarrier(carrier string) string {
	switch carrier {
	case "unicom":
		return "9929"
	case "mobile":
		return "CMIN2"
	default:
		return "CN2 GIA"
	}
}

func insertMainlandSamples(t *testing.T, db *gorm.DB, task models.ReturnRouteTask, outcome string, n int, now time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		sample := models.ReturnRouteProbeSample{
			TaskId: task.Id, Client: task.Client, Carrier: task.Carrier, IPVersion: task.IPVersion,
			Outcome: outcome, ClassifiedLine: task.ExpectedLine, LineState: mainlandLineStateStable,
			CheckedAt: now.Add(-time.Duration(i) * time.Minute),
		}
		if outcome == mainlandOutcomeTruncated {
			sample.TerminalAnchor = "AS4134 202.97.20.0/24"
		}
		if err := db.Create(&sample).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestClassifyMainlandReachabilityOutcomes(t *testing.T) {
	status := giaBaselineStatus()
	reachable := classifyMainlandReachability("", []mainlandPathHop{publicMainlandHop(12, 4809, "203.208.0.8")}, "CN2 GIA", "CN2 GIA", status, true, "203.208.1.1")
	if reachable.Outcome != mainlandOutcomeReachable || reachable.ClassifiedLine != "CN2 GIA" {
		t.Fatalf("reachable = %#v", reachable)
	}
	truncated := classifyMainlandReachability("", []mainlandPathHop{
		publicMainlandHop(5, 4134, "202.97.10.8"), publicMainlandHop(8, 4134, "202.97.20.8"),
		timeoutMainlandHop(9), timeoutMainlandHop(10), timeoutMainlandHop(11),
	}, "CN2 GIA", "CN2 GIA", status, false, "")
	if truncated.Outcome != mainlandOutcomeTruncated {
		t.Fatalf("truncated = %#v", truncated)
	}
	invalid := classifyMainlandReachability("need CAP_NET_RAW", nil, "UNKNOWN", "CN2 GIA", status, false, "")
	if invalid.Outcome != mainlandOutcomeInvalid {
		t.Fatalf("agent error = %s", invalid.Outcome)
	}
	dns := classifyMainlandReachability("resolve: no such host", nil, "UNKNOWN", "CN2 GIA", status, false, "")
	if dns.Outcome != mainlandOutcomeInvalid {
		t.Fatalf("dns error = %s", dns.Outcome)
	}
}

func TestWriteMainlandProbeSampleRespectsFlag(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-off", Token: "t", Name: "Off"}).Error; err != nil {
		t.Fatal(err)
	}
	off := createMainlandTask(t, db, "off", "node-off", "telecom", 4, false)
	now := time.Now().UTC()
	if err := writeMainlandProbeSample(db, off, mainlandProbeClassification{Outcome: mainlandOutcomeTruncated}, now); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&models.ReturnRouteProbeSample{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("disabled flag still wrote %d samples", count)
	}
}

func TestMainlandBlockedRequiresTwoCarriersAndTwoCalculations(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-a", Token: "t", Name: "VMRack_LAX"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-a", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-a", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeTruncated, 2, now)

	if err := evaluateMainlandReachability(db, "node-a", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ? AND ip_version = ?", "node-a", 4).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != mainlandStateObserving || row.Display != mainlandDisplayNormal {
		t.Fatalf("first match = %#v", row)
	}
	if len(*calls) != 0 {
		t.Fatalf("first match notified: %#v", *calls)
	}

	if err := evaluateMainlandReachability(db, "node-a", 4, unicom, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client = ? AND ip_version = ?", "node-a", 4).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != mainlandStateSuspectedBlocked {
		t.Fatalf("second match state = %s", row.State)
	}
	if len(*calls) != 1 || (*calls)[0].kind != mainlandEventBlocked {
		t.Fatalf("second match notifies once, got %#v", *calls)
	}
	if !strings.Contains((*calls)[0].body, "中国电信") || !strings.Contains((*calls)[0].body, "中国联通") {
		t.Fatalf("notify body = %s", (*calls)[0].body)
	}

	if err := evaluateMainlandReachability(db, "node-a", 4, telecom, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("cooldown should suppress repeat, got %#v", *calls)
	}
}

func TestMainlandSingleCarrierDoesNotNotify(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-b", Token: "t", Name: "SG"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-b", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-b", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-b", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ?", "node-b").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Display != mainlandDisplaySingleCarrier || row.State != mainlandStateNormal {
		t.Fatalf("single carrier = %#v", row)
	}
	if len(*calls) != 0 {
		t.Fatalf("single carrier notified: %#v", *calls)
	}
}

func TestMainlandSameCarrierMultipleTasksCountOnce(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-c", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	a := createMainlandTask(t, db, "telecom-1", "node-c", "telecom", 4, true)
	b := createMainlandTask(t, db, "telecom-2", "node-c", "telecom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, a, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, b, mainlandOutcomeTruncated, 2, now)
	if err := evaluateMainlandReachability(db, "node-c", 4, a, now); err != nil {
		t.Fatal(err)
	}
	if err := evaluateMainlandReachability(db, "node-c", 4, b, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ?", "node-c").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Display != mainlandDisplayInsufficient && row.Display != mainlandDisplaySingleCarrier {
		t.Fatalf("same carrier should not look blocked: %#v", row)
	}
	if len(*calls) != 0 {
		t.Fatalf("same carrier notified: %#v", *calls)
	}
}

func TestMainlandInvalidSamplesAreIgnored(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	_ = captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-d", Token: "t", Name: "HK"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-d", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-d", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeInvalid, 4, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeInvalid, 4, now)
	if err := evaluateMainlandReachability(db, "node-d", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ?", "node-d").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Display == mainlandDisplaySuspectedBlocked || row.State == mainlandStateSuspectedBlocked {
		t.Fatalf("invalid samples voted: %#v", row)
	}
}

func TestMainlandOfflineDoesNotNotify(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	mainlandClientOnline = func(string) bool { return false }
	if err := db.Create(&models.Client{UUID: "node-e", Token: "t", Name: "Off"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-e", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-e", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeTruncated, 2, now)
	if err := evaluateMainlandReachability(db, "node-e", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	if err := evaluateMainlandReachability(db, "node-e", 4, unicom, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ?", "node-e").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Display != mainlandDisplayUndetermined {
		t.Fatalf("offline display = %s", row.Display)
	}
	if len(*calls) != 0 {
		t.Fatalf("offline notified: %#v", *calls)
	}
}

func TestMainlandRecoveryNotifiesOnce(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-f", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-f", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-f", "unicom", 4, true)
	now := time.Now().UTC()
	started := now.Add(-time.Hour)
	if err := db.Create(&models.ReturnRouteReachabilityStatus{
		Client: "node-f", IPVersion: 4, State: mainlandStateSuspectedBlocked,
		Display: mainlandDisplaySuspectedBlocked, FailedCarriers: models.StringArray{"telecom", "unicom"},
		AbnormalStartedAt: &started, LastNotifiedAt: &started,
	}).Error; err != nil {
		t.Fatal(err)
	}
	insertMainlandSamples(t, db, telecom, mainlandOutcomeReachable, 3, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 3, now)
	if err := evaluateMainlandReachability(db, "node-f", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ?", "node-f").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != mainlandStateNormal || row.Display != mainlandDisplayNormal {
		t.Fatalf("recovery state = %#v", row)
	}
	if len(*calls) != 1 || (*calls)[0].kind != mainlandEventRecovery {
		t.Fatalf("recovery notify = %#v", *calls)
	}
	if err := evaluateMainlandReachability(db, "node-f", 4, unicom, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("recovery notified twice: %#v", *calls)
	}
}

func TestMainlandRestartDoesNotRepeatFirstAlert(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-g", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-g", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-g", "unicom", 4, true)
	now := time.Now().UTC()
	notified := now.Add(-time.Minute)
	if err := db.Create(&models.ReturnRouteReachabilityStatus{
		Client: "node-g", IPVersion: 4, State: mainlandStateSuspectedBlocked,
		Display: mainlandDisplaySuspectedBlocked, FailedCarriers: models.StringArray{"telecom", "unicom"},
		LastNotifiedAt: &notified, AbnormalStartedAt: &notified,
	}).Error; err != nil {
		t.Fatal(err)
	}
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeTruncated, 2, now)
	if err := evaluateMainlandReachability(db, "node-g", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("restart re-sent first alert: %#v", *calls)
	}
}

func TestMainlandIPv4AndIPv6AreIndependent(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-h", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	v4t := createMainlandTask(t, db, "t4", "node-h", "telecom", 4, true)
	v4u := createMainlandTask(t, db, "u4", "node-h", "unicom", 4, true)
	v6t := createMainlandTask(t, db, "t6", "node-h", "telecom", 6, true)
	v6u := createMainlandTask(t, db, "u6", "node-h", "unicom", 6, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, v4t, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, v4u, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, v6t, mainlandOutcomeReachable, 2, now)
	insertMainlandSamples(t, db, v6u, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-h", 4, v4t, now); err != nil {
		t.Fatal(err)
	}
	if err := evaluateMainlandReachability(db, "node-h", 4, v4u, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := evaluateMainlandReachability(db, "node-h", 6, v6t, now); err != nil {
		t.Fatal(err)
	}
	var v4, v6 models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ? AND ip_version = ?", "node-h", 4).First(&v4).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client = ? AND ip_version = ?", "node-h", 6).First(&v6).Error; err != nil {
		t.Fatal(err)
	}
	if v4.State != mainlandStateSuspectedBlocked {
		t.Fatalf("ipv4 = %#v", v4)
	}
	if v6.Display == mainlandDisplaySuspectedBlocked {
		t.Fatalf("ipv6 should stay independent: %#v", v6)
	}
	if len(*calls) != 1 {
		t.Fatalf("ipv4/v6 notify = %#v", *calls)
	}
}

func TestCleanupMainlandReachabilityDataDropsExpiredAndInactiveSamples(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-i", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	active := createMainlandTask(t, db, "on", "node-i", "telecom", 4, true)
	off := createMainlandTask(t, db, "off", "node-i", "unicom", 4, false)
	now := time.Now().UTC()
	if err := db.Create(&[]models.ReturnRouteProbeSample{
		{TaskId: active.Id, Client: active.Client, Carrier: active.Carrier, IPVersion: 4, Outcome: mainlandOutcomeReachable, CheckedAt: now.Add(-25 * time.Hour)},
		{TaskId: active.Id, Client: active.Client, Carrier: active.Carrier, IPVersion: 4, Outcome: mainlandOutcomeReachable, CheckedAt: now.Add(-time.Minute)},
		{TaskId: off.Id, Client: off.Client, Carrier: off.Carrier, IPVersion: 4, Outcome: mainlandOutcomeTruncated, CheckedAt: now.Add(-time.Minute)},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ReturnRouteReachabilityStatus{Client: "gone", IPVersion: 4, State: mainlandStateNormal, Display: mainlandDisplayInsufficient}).Error; err != nil {
		t.Fatal(err)
	}
	if err := cleanupMainlandReachabilityData(db, now); err != nil {
		t.Fatal(err)
	}
	var samples int64
	if err := db.Model(&models.ReturnRouteProbeSample{}).Count(&samples).Error; err != nil {
		t.Fatal(err)
	}
	if samples != 1 {
		t.Fatalf("kept %d samples, want only the fresh active one", samples)
	}
	var leftover int64
	if err := db.Model(&models.ReturnRouteReachabilityStatus{}).Where("client = ?", "gone").Count(&leftover).Error; err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatal("orphaned reachability row was kept")
	}
}

func TestQueryReturnRouteFiltersAndSummaryIncludeReachability(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-a", Token: "token-a", Name: "Tokyo-01"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "Tokyo Telecom", "node-a", "telecom", 4, true)
	if err := db.Model(&models.ReturnRouteReachabilityStatus{}).Create(&models.ReturnRouteReachabilityStatus{
		Client: "node-a", IPVersion: 4, State: mainlandStateSuspectedBlocked, Display: mainlandDisplaySuspectedBlocked,
	}).Error; err != nil {
		t.Fatal(err)
	}
	page, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{State: "suspected_blocked"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Reachability) != 1 || page.Reachability[0].Display != mainlandDisplaySuspectedBlocked {
		t.Fatalf("blocked filter = %#v", page)
	}
	summary, err := getReturnRouteSummary(db, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if summary.SuspectedBlocked != 1 {
		t.Fatalf("summary blocked = %#v", summary)
	}
	_ = task
}

func TestFilterReturnRouteEventsAcceptsMainlandKinds(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-a", Token: "t", Name: "Tokyo-01"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "t", "node-a", "telecom", 4, true)
	if err := db.Create(&models.ReturnRouteEvent{
		TaskId: task.Id, Client: "node-a", Kind: mainlandEventBlocked, ToLine: mainlandLineBlocked,
		Confidence: 1, OccurredAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	page, err := queryReturnRouteEvents(db, ReturnRouteEventQuery{Kind: mainlandEventBlocked})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("mainland event filter = %#v", page)
	}
}

func TestEditReturnRouteTasksBatchUpdatesReachabilityFlags(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-a", Token: "t", Name: "Tokyo-01"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "t", "node-a", "telecom", 4, false)
	err := editReturnRouteTasksBatch(db, ReturnRouteTaskBatchEdit{
		IDs: []uint{task.Id}, Carrier: "telecom", Region: "华东", Target: "1.1.1.1",
		IPVersion: 4, ExpectedLine: "CN2 GIA", Protocol: "icmp", Interval: 180,
		SwitchConfirm: 2, RecoveryConfirm: 3, Cooldown: 1800, Notify: true, NotifyRecovery: true,
		MainlandReachabilityEnabled: true, MainlandReachabilityNotify: false, MainlandReachabilityRecoveryNotify: true,
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var updated models.ReturnRouteTask
	if err := db.First(&updated, task.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !updated.MainlandReachabilityEnabled || updated.MainlandReachabilityNotify || !updated.MainlandReachabilityRecoveryNotify {
		t.Fatalf("batch flags = %#v", updated)
	}
}

func TestMainlandSwitchSamplesDoNotVote(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-sw", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-sw", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-sw", "unicom", 4, true)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		if err := db.Create(&models.ReturnRouteProbeSample{
			TaskId: telecom.Id, Client: telecom.Client, Carrier: telecom.Carrier, IPVersion: 4,
			Outcome: mainlandOutcomeIndeterminate, ClassifiedLine: "CUG VIP", LineState: mainlandLineStateSwitching,
			CheckedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertMainlandSamples(t, db, unicom, mainlandOutcomeTruncated, 2, now)
	if err := evaluateMainlandReachability(db, "node-sw", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ?", "node-sw").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Display == mainlandDisplaySuspectedBlocked || row.State == mainlandStateSuspectedBlocked {
		t.Fatalf("switch samples voted as blocked: %#v", row)
	}
	if row.Display != mainlandDisplaySingleCarrier {
		t.Fatalf("unicom-only truncation should be single carrier, got %#v", row)
	}
	if len(*calls) != 0 {
		t.Fatalf("switch samples notified: %#v", *calls)
	}
}

func TestMainlandSwitchSamplesDoNotCountAsRecovery(t *testing.T) {
	samples := []models.ReturnRouteProbeSample{
		{Outcome: mainlandOutcomeReachable, LineState: mainlandLineStateStable},
		{Outcome: mainlandOutcomeIndeterminate, LineState: mainlandLineStateSwitching},
		{Outcome: mainlandOutcomeReachable, LineState: mainlandLineStateStable},
	}
	if got := consecutiveMainlandReachable(samples); got != 1 {
		t.Fatalf("switch sample must break recovery streak, got %d", got)
	}
	recovered := []models.ReturnRouteProbeSample{
		{Outcome: mainlandOutcomeReachable, LineState: mainlandLineStateStable},
		{Outcome: mainlandOutcomeReachable, LineState: mainlandLineStateStable},
		{Outcome: mainlandOutcomeReachable, LineState: mainlandLineStateStable},
	}
	if got := consecutiveMainlandReachable(recovered); got != 3 {
		t.Fatalf("reachable streak = %d", got)
	}
	if got := consecutiveMainlandReachable([]models.ReturnRouteProbeSample{
		{Outcome: mainlandOutcomeReachable, LineState: mainlandLineStateStable},
		{Outcome: mainlandOutcomeTruncated, LineState: mainlandLineStateStable},
	}); got != 1 {
		t.Fatalf("truncated must not increase recovery, got %d", got)
	}
}

