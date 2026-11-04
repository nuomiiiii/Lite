package tasks

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newPublicReturnRouteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:return-route-public-%s-%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Client{},
		&models.ReturnRouteTask{},
		&models.ReturnRouteStatus{},
		&models.ReturnRouteReachabilityStatus{},
	); err != nil {
		t.Fatal(err)
	}
	clients := []models.Client{
		{UUID: "node-visible", Token: "token-visible", Name: "Visible"},
		{UUID: "node-hidden", Token: "token-hidden", Name: "Hidden", Hidden: true},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func seedPublicReturnRouteTask(t *testing.T, db *gorm.DB, client, carrier, expected, current string, ipVersion int, enabled bool) models.ReturnRouteTask {
	t.Helper()
	task := models.ReturnRouteTask{
		Name:            client + "_" + carrier,
		Client:          client,
		Carrier:         carrier,
		Region:          "华东",
		Target:          "120.232.0.1",
		IPVersion:       ipVersion,
		ExpectedLine:    expected,
		Protocol:        "icmp",
		Interval:        180,
		SwitchConfirm:   2,
		RecoveryConfirm: 3,
		Cooldown:        1800,
		Enabled:         enabled,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if !enabled {
		if err := db.Model(&task).Update("enabled", false).Error; err != nil {
			t.Fatal(err)
		}
		task.Enabled = false
	}
	if current == "" {
		return task
	}
	status := models.ReturnRouteStatus{TaskId: task.Id, CurrentLine: current, State: "healthy"}
	if err := db.Create(&status).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

func TestListPublicReturnRoutesExposesOnlyCurrentLinesAndBlock(t *testing.T) {
	db := newPublicReturnRouteTestDB(t)
	seedPublicReturnRouteTask(t, db, "node-visible", "mobile", "CMIN2", "CMNET", 4, true)
	seedPublicReturnRouteTask(t, db, "node-visible", "mobile", "CMIN2", "CMNET", 6, true)
	seedPublicReturnRouteTask(t, db, "node-visible", "telecom", "CN2 GIA", "CN2 GIA", 4, true)
	seedPublicReturnRouteTask(t, db, "node-visible", "unicom", "CUG VIP", "", 4, true)
	seedPublicReturnRouteTask(t, db, "node-visible", "unicom", "163", "163", 4, false)
	seedPublicReturnRouteTask(t, db, "node-hidden", "mobile", "CMIN2", "CMIN2", 4, true)
	if err := db.Create(&models.ReturnRouteReachabilityStatus{
		Client:         "node-visible",
		IPVersion:      4,
		State:          mainlandStateSuspectedBlocked,
		Display:        mainlandDisplaySuspectedBlocked,
		HighConfidence: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.ReturnRouteReachabilityStatus{
		Client:    "node-hidden",
		IPVersion: 4,
		State:     mainlandStateSuspectedBlocked,
		Display:   mainlandDisplaySuspectedBlocked,
	}).Error; err != nil {
		t.Fatal(err)
	}

	out, err := listPublicReturnRoutes(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("public routes = %#v", out)
	}
	item := out[0]
	if item.Client != "node-visible" {
		t.Fatalf("client = %q", item.Client)
	}
	if strings.Join(item.Lines, ",") != "CMNET,CN2 GIA" {
		t.Fatalf("lines = %#v", item.Lines)
	}
	if len(item.Tasks) != 0 {
		t.Fatalf("unbound ping tasks = %#v", item.Tasks)
	}
	if !item.SuspectedBlocked || !item.HighConfidence {
		t.Fatalf("blocked flags = %#v", item)
	}

	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	for _, forbidden := range []string{
		"expected_line",
		"ip_version",
		"route_path",
		"asn_path",
		"target",
		"hops",
		"carrier",
		"120.232.0.1",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("public payload leaked %q: %s", forbidden, payload)
		}
	}
}

func TestListPublicReturnRoutesIncludesHiddenWhenRequested(t *testing.T) {
	db := newPublicReturnRouteTestDB(t)
	seedPublicReturnRouteTask(t, db, "node-hidden", "telecom", "CN2 GIA", "CN2 GIA", 4, true)

	hidden, err := listPublicReturnRoutes(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 0 {
		t.Fatalf("guest saw hidden node: %#v", hidden)
	}

	admin, err := listPublicReturnRoutes(db, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(admin) != 1 || admin[0].Client != "node-hidden" || strings.Join(admin[0].Lines, ",") != "CN2 GIA" {
		t.Fatalf("admin routes = %#v", admin)
	}
}

func TestListPublicReturnRoutesKeepsBlockedWithoutLine(t *testing.T) {
	db := newPublicReturnRouteTestDB(t)
	if err := db.Create(&models.ReturnRouteReachabilityStatus{
		Client:    "node-visible",
		IPVersion: 6,
		State:     mainlandStateSuspectedBlocked,
		Display:   mainlandDisplaySuspectedBlocked,
	}).Error; err != nil {
		t.Fatal(err)
	}

	out, err := listPublicReturnRoutes(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Client != "node-visible" || len(out[0].Lines) != 0 || !out[0].SuspectedBlocked || out[0].HighConfidence {
		t.Fatalf("blocked-only route = %#v", out)
	}
}

func TestListPublicReturnRoutesMapsAssistPingTasks(t *testing.T) {
	db := newPublicReturnRouteTestDB(t)
	mobileV4 := uint(11)
	mobileV6 := uint(11)
	telecom := uint(12)
	seed := seedPublicReturnRouteTask(t, db, "node-visible", "mobile", "CMIN2", "CMNET", 4, true)
	if err := db.Model(&seed).Updates(map[string]any{
		"mainland_reachability_enabled":      true,
		"mainland_reachability_ping_task_id": mobileV4,
	}).Error; err != nil {
		t.Fatal(err)
	}
	v6 := seedPublicReturnRouteTask(t, db, "node-visible", "mobile", "CMIN2", "CMIN2", 6, true)
	if err := db.Model(&v6).Updates(map[string]any{
		"mainland_reachability_enabled":      true,
		"mainland_reachability_ping_task_id": mobileV6,
	}).Error; err != nil {
		t.Fatal(err)
	}
	gia := seedPublicReturnRouteTask(t, db, "node-visible", "telecom", "CN2 GIA", "CN2 GIA", 4, true)
	if err := db.Model(&gia).Updates(map[string]any{
		"mainland_reachability_enabled":      true,
		"mainland_reachability_ping_task_id": telecom,
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedPublicReturnRouteTask(t, db, "node-visible", "unicom", "163", "163", 4, true)

	out, err := listPublicReturnRoutes(db, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("public routes = %#v", out)
	}
	item := out[0]
	if strings.Join(item.Lines, ",") != "CMNET,CN2 GIA,163,CMIN2" {
		t.Fatalf("lines = %#v", item.Lines)
	}
	if len(item.Tasks) != 2 {
		t.Fatalf("tasks = %#v", item.Tasks)
	}
	if item.Tasks[0].PingTaskID != 11 || item.Tasks[0].Line != "CMNET" {
		t.Fatalf("ipv4 ping mapping = %#v", item.Tasks[0])
	}
	if item.Tasks[1].PingTaskID != 12 || item.Tasks[1].Line != "CN2 GIA" {
		t.Fatalf("telecom ping mapping = %#v", item.Tasks[1])
	}
}
