package tasks

import (
	"fmt"
	"testing"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

	legacy, err := queryReturnRouteEvents(db, ReturnRouteEventQuery{Keyword: "210.13.64.1", ExpectedLine: "9929", Region: "华东"})
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
