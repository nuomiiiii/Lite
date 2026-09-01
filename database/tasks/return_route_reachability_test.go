package tasks

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
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
		&models.PingTask{},
		&models.ReturnRouteTask{},
		&models.ReturnRouteStatus{},
		&models.ReturnRouteEvent{},
		&models.ReturnRouteProbeSample{},
		&models.ReturnRouteReachabilityStatus{},
	); err != nil {
		t.Fatal(err)
	}
	mainlandPingLossTestHook = func(string, uint, time.Time, time.Time) (metricstore.PingLossStats, error) {
		return metricstore.PingLossStats{Total: 2, Lost: 2}, nil
	}
	prevOnline := mainlandClientOnline
	mainlandClientOnline = func(string) bool { return true }
	t.Cleanup(func() {
		mainlandPingLossTestHook = nil
		mainlandClientOnline = prevOnline
	})
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
	if enabled {
		ping := models.PingTask{
			Name: name + "-ping", Clients: models.StringArray{client},
			Type: "icmp", Target: "1.1.1.1", Interval: 60,
		}
		if err := db.Create(&ping).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&task).Update("mainland_reachability_ping_task_id", ping.Id).Error; err != nil {
			t.Fatal(err)
		}
		task.MainlandReachabilityPingTaskID = &ping.Id
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
			BaselineVersion: 1, CheckedAt: now.Add(-time.Duration(i) * time.Minute),
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
	on := createMainlandTask(t, db, "on-no-ping", "node-off", "unicom", 4, true)
	if err := db.Model(&on).Update("mainland_reachability_ping_task_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	on.MainlandReachabilityPingTaskID = nil
	if err := writeMainlandProbeSample(db, on, mainlandProbeClassification{Outcome: mainlandOutcomeTruncated}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ReturnRouteProbeSample{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("enabled task without ping still wrote %d samples", count)
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

func TestMainlandOptOutSiblingsDoNotParticipate(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-multi", Token: "t", Name: "LAX-02"}).Error; err != nil {
		t.Fatal(err)
	}
	telecomA := createMainlandTask(t, db, "telecom-a", "node-multi", "telecom", 4, true)
	telecomB := createMainlandTask(t, db, "telecom-b", "node-multi", "telecom", 4, false)
	unicom := createMainlandTask(t, db, "unicom", "node-multi", "unicom", 4, true)
	mobile := createMainlandTask(t, db, "mobile", "node-multi", "mobile", 4, false)
	if err := db.Model(&telecomB).Update("target", "8.8.8.8").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&mobile).Update("target", "114.114.114.114").Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecomA, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, telecomB, mainlandOutcomeTruncated, 4, now)
	insertMainlandSamples(t, db, mobile, mainlandOutcomeTruncated, 4, now)

	if err := evaluateMainlandReachability(db, "node-multi", 4, telecomA, now); err != nil {
		t.Fatal(err)
	}
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ? AND ip_version = ?", "node-multi", 4).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != mainlandStateObserving {
		t.Fatalf("two opted-in carriers should start observing, got %#v", row)
	}
	if err := evaluateMainlandReachability(db, "node-multi", 4, unicom, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("client = ? AND ip_version = ?", "node-multi", 4).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != mainlandStateSuspectedBlocked {
		t.Fatalf("opt-out siblings should not block two opted-in carriers: %#v", row)
	}
	if len(*calls) != 1 {
		t.Fatalf("notify = %#v", *calls)
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
	if samples != 2 {
		t.Fatalf("kept %d samples, want the fresh active and still-recent disabled samples", samples)
	}
	var leftover int64
	if err := db.Model(&models.ReturnRouteReachabilityStatus{}).Where("client = ?", "gone").Count(&leftover).Error; err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatal("orphaned reachability row was kept")
	}
}

func TestQueryReturnRouteReachabilityFilterSkipsOptOutTasks(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-opt", Token: "token-opt", Name: "LAX-02"}).Error; err != nil {
		t.Fatal(err)
	}
	onA := createMainlandTask(t, db, "LAX CT", "node-opt", "telecom", 4, true)
	onB := createMainlandTask(t, db, "LAX CM", "node-opt", "mobile", 4, true)
	off := createMainlandTask(t, db, "LAX CU", "node-opt", "unicom", 4, false)
	extraIP := createMainlandTask(t, db, "LAX CT extra", "node-opt", "telecom", 4, false)
	if err := db.Model(&extraIP).Update("target", "203.0.113.10").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ReturnRouteReachabilityStatus{}).Create(&models.ReturnRouteReachabilityStatus{
		Client: "node-opt", IPVersion: 4, State: mainlandStateNormal, Display: mainlandDisplayCollecting,
	}).Error; err != nil {
		t.Fatal(err)
	}
	page, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{State: "insufficient"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("collecting node should not match insufficient filter: %#v", page)
	}
	if err := db.Model(&models.ReturnRouteReachabilityStatus{}).Where("client = ?", "node-opt").
		Update("display", mainlandDisplaySuspectedBlocked).Error; err != nil {
		t.Fatal(err)
	}
	blocked, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{State: "suspected_blocked"})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Total != 2 {
		t.Fatalf("blocked filter total = %d, want 2 opted-in tasks", blocked.Total)
	}
	seen := map[uint]bool{}
	for _, task := range blocked.Tasks {
		seen[task.Id] = true
	}
	if !seen[onA.Id] || !seen[onB.Id] || seen[off.Id] || seen[extraIP.Id] {
		t.Fatalf("blocked filter ids = %#v, opt-out tasks %d/%d should be excluded", blocked.Tasks, off.Id, extraIP.Id)
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

func TestQueryReturnRouteTasksHidesMissingAndUnassignedMainlandPing(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-hide", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	missing := createMainlandTask(t, db, "missing-ping", "node-hide", "telecom", 4, true)
	unassigned := createMainlandTask(t, db, "unassigned-ping", "node-hide", "unicom", 4, true)
	ready := createMainlandTask(t, db, "ready-ping", "node-hide", "mobile", 4, true)
	gone := uint(99999)
	if err := db.Model(&missing).Update("mainland_reachability_ping_task_id", gone).Error; err != nil {
		t.Fatal(err)
	}
	var ping models.PingTask
	if err := db.First(&ping, *unassigned.MainlandReachabilityPingTaskID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&ping).Update("clients", models.StringArray{"other-node"}).Error; err != nil {
		t.Fatal(err)
	}

	page, err := queryReturnRouteTasks(db, ReturnRouteTaskQuery{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint]models.ReturnRouteTask{}
	for _, task := range page.Tasks {
		byID[task.Id] = task
	}
	if byID[missing.Id].MainlandReachabilityPingTaskID != nil {
		t.Fatalf("deleted ping should be hidden: %#v", byID[missing.Id].MainlandReachabilityPingTaskID)
	}
	if byID[unassigned.Id].MainlandReachabilityPingTaskID != nil {
		t.Fatalf("unassigned ping should be hidden: %#v", byID[unassigned.Id].MainlandReachabilityPingTaskID)
	}
	if byID[ready.Id].MainlandReachabilityPingTaskID == nil || *byID[ready.Id].MainlandReachabilityPingTaskID == 0 {
		t.Fatalf("valid ping was hidden: %#v", byID[ready.Id])
	}

	var stored models.ReturnRouteTask
	if err := db.First(&stored, missing.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.MainlandReachabilityPingTaskID == nil || *stored.MainlandReachabilityPingTaskID != gone {
		t.Fatalf("stored missing ping id = %#v", stored.MainlandReachabilityPingTaskID)
	}
}

func TestDeletePingTaskClearsReturnRouteMainlandPing(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.AutoMigrate(&models.MetricCleanupJob{}, &models.PingLossNotification{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Client{UUID: "node-clear", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "clear-ping", "node-clear", "telecom", 4, true)
	keep := createMainlandTask(t, db, "keep-ping", "node-clear", "unicom", 4, true)
	pingID := *task.MainlandReachabilityPingTaskID
	keepPing := *keep.MainlandReachabilityPingTaskID
	if err := deletePingTaskRows(db, []uint{pingID}); err != nil {
		t.Fatal(err)
	}
	var stored models.ReturnRouteTask
	if err := db.First(&stored, task.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.MainlandReachabilityPingTaskID != nil {
		t.Fatalf("deleted ping association kept: %#v", stored.MainlandReachabilityPingTaskID)
	}
	var keepStored models.ReturnRouteTask
	if err := db.First(&keepStored, keep.Id).Error; err != nil {
		t.Fatal(err)
	}
	if keepStored.MainlandReachabilityPingTaskID == nil || *keepStored.MainlandReachabilityPingTaskID != keepPing {
		t.Fatalf("other task ping = %#v", keepStored.MainlandReachabilityPingTaskID)
	}
	if err := db.First(&models.PingTask{}, pingID).Error; err != gorm.ErrRecordNotFound {
		t.Fatalf("ping task still present: %v", err)
	}
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
	ping := models.PingTask{Name: "assist", Clients: models.StringArray{"node-a"}, Type: "icmp", Target: "1.1.1.1", Interval: 60}
	if err := db.Create(&ping).Error; err != nil {
		t.Fatal(err)
	}
	err := editReturnRouteTasksBatch(db, ReturnRouteTaskBatchEdit{
		IDs: []uint{task.Id}, Carrier: "telecom", Region: "华东", Target: "1.1.1.1",
		IPVersion: 4, ExpectedLine: "CN2 GIA", Protocol: "icmp", Interval: 180,
		SwitchConfirm: 2, RecoveryConfirm: 3, Cooldown: 1800, Notify: true, NotifyRecovery: true,
		MainlandReachabilityEnabled: true, MainlandReachabilityNotify: false, MainlandReachabilityRecoveryNotify: true,
		MainlandReachabilityPingTaskID: &ping.Id,
		Enabled:                        true,
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
	if updated.MainlandReachabilityPingTaskID == nil || *updated.MainlandReachabilityPingTaskID != ping.Id {
		t.Fatalf("batch ping task = %#v", updated.MainlandReachabilityPingTaskID)
	}
}

func TestEditReturnRouteTaskPersistsMainlandPing(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-edit", Token: "t", Name: "TW-01"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "edit", "node-edit", "mobile", 4, false)
	ping := models.PingTask{Name: "assist", Clients: models.StringArray{"node-edit"}, Type: "icmp", Target: "1.1.1.1", Interval: 60}
	if err := db.Create(&ping).Error; err != nil {
		t.Fatal(err)
	}
	task.MainlandReachabilityEnabled = true
	task.MainlandReachabilityNotify = true
	task.MainlandReachabilityRecoveryNotify = true
	task.MainlandReachabilityPingTaskID = &ping.Id
	if err := editReturnRouteTask(db, &task); err != nil {
		t.Fatal(err)
	}
	var updated models.ReturnRouteTask
	if err := db.First(&updated, task.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !updated.MainlandReachabilityEnabled {
		t.Fatalf("enabled was not persisted: %#v", updated)
	}
	if updated.MainlandReachabilityPingTaskID == nil || *updated.MainlandReachabilityPingTaskID != ping.Id {
		t.Fatalf("ping task was not persisted: %#v", updated.MainlandReachabilityPingTaskID)
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

func stubMainlandPingLoss(t *testing.T, stats metricstore.PingLossStats) {
	t.Helper()
	mainlandPingLossTestHook = func(string, uint, time.Time, time.Time) (metricstore.PingLossStats, error) {
		return stats, nil
	}
}

func firstReachability(t *testing.T, db *gorm.DB, client string) models.ReturnRouteReachabilityStatus {
	t.Helper()
	var row models.ReturnRouteReachabilityStatus
	if err := db.Where("client = ?", client).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestMainlandTruncationWithHealthyPingStaysNormal(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	stubMainlandPingLoss(t, metricstore.PingLossStats{Total: 10, Lost: 0})
	if err := db.Create(&models.Client{UUID: "node-path", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-path", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-path", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-path", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-path")
	if row.Display != mainlandDisplayNormal || row.State != mainlandStateNormal {
		t.Fatalf("truncation without ping loss should stay normal: %#v", row)
	}
}

func TestMainlandHealthyPathWithHighPingLossStaysNormal(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	stubMainlandPingLoss(t, metricstore.PingLossStats{Total: 10, Lost: 10})
	if err := db.Create(&models.Client{UUID: "node-ping", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-ping", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-ping", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeReachable, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-ping", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-ping")
	if row.Display != mainlandDisplayNormal {
		t.Fatalf("high ping loss without truncation should stay normal: %#v", row)
	}
}

func TestMainlandTruncationAndPingLossIsSingleCarrier(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	stubMainlandPingLoss(t, metricstore.PingLossStats{Total: 2, Lost: 2})
	if err := db.Create(&models.Client{UUID: "node-combo", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-combo", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-combo", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-combo", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-combo")
	if row.Display != mainlandDisplaySingleCarrier || row.State != mainlandStateNormal {
		t.Fatalf("combined condition should be single carrier: %#v", row)
	}
}

func TestMainlandTwoCarriersCombinedEnterBlockedState(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-both", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-both", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-both", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeTruncated, 2, now)
	if err := evaluateMainlandReachability(db, "node-both", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	if err := evaluateMainlandReachability(db, "node-both", 4, unicom, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-both")
	if row.State != mainlandStateSuspectedBlocked {
		t.Fatalf("two combined carriers should enter blocked: %#v", row)
	}
	if len(*calls) != 1 {
		t.Fatalf("notify = %#v", *calls)
	}
}

func TestMainlandPingSamplesBelowTwoDoNotConfirm(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	stubMainlandPingLoss(t, metricstore.PingLossStats{Total: 1, Lost: 1})
	if err := db.Create(&models.Client{UUID: "node-few", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-few", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-few", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-few", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-few")
	if row.Display == mainlandDisplaySingleCarrier {
		t.Fatalf("one ping sample should not confirm: %#v", row)
	}
}

func TestMainlandMissingOrUnassignedPingDoesNotConfirm(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-miss", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-miss", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-miss", "unicom", 4, true)
	if err := db.Model(&telecom).Update("mainland_reachability_ping_task_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	telecom.MainlandReachabilityPingTaskID = nil
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-miss", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-miss")
	if row.Display == mainlandDisplaySingleCarrier {
		t.Fatalf("missing ping must not confirm: %#v", row)
	}

	other := uint(99999)
	if err := db.Model(&unicom).Update("mainland_reachability_ping_task_id", other).Error; err != nil {
		t.Fatal(err)
	}
	insertMainlandSamples(t, db, unicom, mainlandOutcomeTruncated, 2, now.Add(time.Minute))
	if err := evaluateMainlandReachability(db, "node-miss", 4, unicom, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row = firstReachability(t, db, "node-miss")
	if row.Display == mainlandDisplaySuspectedBlocked || row.Display == mainlandDisplaySingleCarrier {
		t.Fatalf("deleted ping task must not confirm: %#v", row)
	}
}

func TestMainlandStaleBaselineSamplesDoNotVote(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-base", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-base", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-base", "unicom", 4, true)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		if err := db.Create(&models.ReturnRouteProbeSample{
			TaskId: telecom.Id, Client: telecom.Client, Carrier: telecom.Carrier, IPVersion: 4,
			Outcome: mainlandOutcomeTruncated, ClassifiedLine: telecom.ExpectedLine, LineState: mainlandLineStateStable,
			BaselineVersion: 0, CheckedAt: now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	if err := evaluateMainlandReachability(db, "node-base", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-base")
	if row.Display == mainlandDisplaySingleCarrier {
		t.Fatalf("old baseline samples voted: %#v", row)
	}
}

func TestMainlandSamePingTaskDoesNotDuplicateCarrierVotes(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	calls := captureMainlandNotify(t)
	if err := db.Create(&models.Client{UUID: "node-share", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	a := createMainlandTask(t, db, "telecom-1", "node-share", "telecom", 4, true)
	b := createMainlandTask(t, db, "telecom-2", "node-share", "telecom", 4, true)
	shared := *a.MainlandReachabilityPingTaskID
	if err := db.Model(&b).Update("mainland_reachability_ping_task_id", shared).Error; err != nil {
		t.Fatal(err)
	}
	b.MainlandReachabilityPingTaskID = &shared
	now := time.Now().UTC()
	insertMainlandSamples(t, db, a, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, b, mainlandOutcomeTruncated, 2, now)
	if err := evaluateMainlandReachability(db, "node-share", 4, a, now); err != nil {
		t.Fatal(err)
	}
	if err := evaluateMainlandReachability(db, "node-share", 4, b, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := firstReachability(t, db, "node-share")
	if row.Display != mainlandDisplayInsufficient && row.Display != mainlandDisplaySingleCarrier {
		t.Fatalf("same carrier plus shared ping must not look blocked: %#v", row)
	}
	if len(*calls) != 0 {
		t.Fatalf("shared ping notified: %#v", *calls)
	}
}

func TestMainlandReachabilityStatusIsUpsertedAndEvidenceReplaced(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-up", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-up", "telecom", 4, true)
	unicom := createMainlandTask(t, db, "unicom", "node-up", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	insertMainlandSamples(t, db, unicom, mainlandOutcomeReachable, 2, now)
	for i := 0; i < 3; i++ {
		if err := evaluateMainlandReachability(db, "node-up", 4, telecom, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&models.ReturnRouteReachabilityStatus{}).Where("client = ? AND ip_version = ?", "node-up", 4).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reachability rows = %d", count)
	}
	row := firstReachability(t, db, "node-up")
	if !strings.Contains(row.CarrierEvidence, `"carrier":"telecom"`) || strings.Count(row.CarrierEvidence, `"evidence"`) != 1 {
		t.Fatalf("evidence should be replaced, got %s", row.CarrierEvidence)
	}
}

func TestCleanupMainlandSamplesRespectsUTCCutoffAndLeavesEvents(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-ttl", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "on", "node-ttl", "telecom", 4, true)
	now := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
	boundary := now.Add(-24 * time.Hour)
	if err := db.Create(&[]models.ReturnRouteProbeSample{
		{TaskId: task.Id, Client: task.Client, Carrier: task.Carrier, IPVersion: 4, Outcome: mainlandOutcomeReachable, CheckedAt: boundary.Add(-time.Second)},
		{TaskId: task.Id, Client: task.Client, Carrier: task.Carrier, IPVersion: 4, Outcome: mainlandOutcomeReachable, CheckedAt: boundary},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ReturnRouteEvent{
		TaskId: task.Id, Client: task.Client, Kind: mainlandEventBlocked, ToLine: mainlandLineBlocked,
		Confidence: 1, OccurredAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := cleanupMainlandReachabilityData(db, now); err != nil {
		t.Fatal(err)
	}
	if err := cleanupMainlandReachabilityData(db, now); err != nil {
		t.Fatal(err)
	}
	var samples []models.ReturnRouteProbeSample
	if err := db.Find(&samples).Error; err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || !samples[0].CheckedAt.Equal(boundary) {
		t.Fatalf("boundary samples = %#v", samples)
	}
	var events int64
	if err := db.Model(&models.ReturnRouteEvent{}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("events deleted by sample cleanup: %d", events)
	}
	var statuses int64
	if err := db.Model(&models.ReturnRouteStatus{}).Count(&statuses).Error; err != nil {
		t.Fatal(err)
	}
	if statuses != 1 {
		t.Fatalf("return route status deleted: %d", statuses)
	}
}

func TestDeleteReturnRouteSamplesDoesNotDeletePingTask(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-del", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "on", "node-del", "telecom", 4, true)
	pingID := *task.MainlandReachabilityPingTaskID
	now := time.Now().UTC()
	insertMainlandSamples(t, db, task, mainlandOutcomeReachable, 2, now)
	if err := db.Where("task_id = ?", task.Id).Delete(&models.ReturnRouteProbeSample{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&task).Error; err != nil {
		t.Fatal(err)
	}
	var ping models.PingTask
	if err := db.First(&ping, pingID).Error; err != nil {
		t.Fatal(err)
	}
	var samples int64
	if err := db.Model(&models.ReturnRouteProbeSample{}).Where("task_id = ?", task.Id).Count(&samples).Error; err != nil {
		t.Fatal(err)
	}
	if samples != 0 {
		t.Fatalf("samples left after delete: %d", samples)
	}
}

func TestDisableMainlandReachabilityStopsWritingSamples(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-stop", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	task := createMainlandTask(t, db, "on", "node-stop", "telecom", 4, true)
	now := time.Now().UTC()
	if err := writeMainlandProbeSample(db, task, mainlandProbeClassification{Outcome: mainlandOutcomeReachable, BaselineVersion: 1}, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&task).Update("mainland_reachability_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	task.MainlandReachabilityEnabled = false
	if err := writeMainlandProbeSample(db, task, mainlandProbeClassification{Outcome: mainlandOutcomeTruncated, BaselineVersion: 1}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&models.ReturnRouteProbeSample{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("disabled task kept writing, count=%d", count)
	}
}

func TestEvaluateMainlandReachabilityQueriesPingWithoutWritingMetrics(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	queries := 0
	mainlandPingLossTestHook = func(string, uint, time.Time, time.Time) (metricstore.PingLossStats, error) {
		queries++
		return metricstore.PingLossStats{Total: 2, Lost: 2}, nil
	}
	if err := db.Create(&models.Client{UUID: "node-q", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	telecom := createMainlandTask(t, db, "telecom", "node-q", "telecom", 4, true)
	_ = createMainlandTask(t, db, "unicom", "node-q", "unicom", 4, true)
	now := time.Now().UTC()
	insertMainlandSamples(t, db, telecom, mainlandOutcomeTruncated, 2, now)
	var pingCount int64
	if err := db.Model(&models.PingTask{}).Count(&pingCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := evaluateMainlandReachability(db, "node-q", 4, telecom, now); err != nil {
		t.Fatal(err)
	}
	var pingCountAfter int64
	if err := db.Model(&models.PingTask{}).Count(&pingCountAfter).Error; err != nil {
		t.Fatal(err)
	}
	if pingCountAfter != pingCount || queries == 0 {
		t.Fatalf("ping query/write mismatch queries=%d ping=%d/%d", queries, pingCount, pingCountAfter)
	}
}

func TestNormalizeReturnRouteRequiresAssignedPingTask(t *testing.T) {
	db := seedMainlandReachabilityDB(t)
	if err := db.Create(&models.Client{UUID: "node-n", Token: "t", Name: "LA"}).Error; err != nil {
		t.Fatal(err)
	}
	ping := models.PingTask{Name: "assist", Clients: models.StringArray{"node-n"}, Type: "icmp", Target: "1.1.1.1", Interval: 60}
	if err := db.Create(&ping).Error; err != nil {
		t.Fatal(err)
	}
	task := models.ReturnRouteTask{
		Name: "t", Client: "node-n", Carrier: "telecom", Region: "华东", Target: "1.1.1.1",
		IPVersion: 4, ExpectedLine: "CN2 GIA", Protocol: "icmp", Interval: 180,
		SwitchConfirm: 2, RecoveryConfirm: 3, MainlandReachabilityEnabled: true,
	}
	if err := normalizeReturnRouteTaskWithDB(db, &task); err == nil {
		t.Fatal("enabled reachability without ping should fail")
	}
	task.MainlandReachabilityPingTaskID = &ping.Id
	if err := normalizeReturnRouteTaskWithDB(db, &task); err != nil {
		t.Fatal(err)
	}
	other := models.PingTask{Name: "other", Clients: models.StringArray{"someone-else"}, Type: "tcp", Target: "8.8.8.8", Interval: 60}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	task.MainlandReachabilityPingTaskID = &other.Id
	if err := normalizeReturnRouteTaskWithDB(db, &task); err == nil {
		t.Fatal("ping task assigned to another server should fail")
	}
}
