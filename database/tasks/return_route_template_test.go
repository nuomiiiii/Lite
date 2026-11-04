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

func newReturnRouteTemplateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:return-route-template-%s-%d?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"), time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ReturnRouteTask{}, &models.ReturnRouteTemplate{}); err != nil {
		t.Fatal(err)
	}
	clients := []models.Client{
		{UUID: "node-a", Token: "token-a", Name: "VMRack_LAX_L3"},
		{UUID: "node-b", Token: "token-b", Name: "HongKong-03"},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func sampleMobileTemplate() models.ReturnRouteTemplate {
	return models.ReturnRouteTemplate{
		Name:            "XX移动 CMIN2",
		Carrier:         "mobile",
		Region:          "华东",
		Target:          "120.232.0.1",
		IPVersion:       4,
		ExpectedLine:    "CMIN2",
		Protocol:        "icmp",
		Interval:        180,
		SwitchConfirm:   2,
		RecoveryConfirm: 3,
		Cooldown:        1800,
		Notify:          true,
		NotifyRecovery:  true,
		Enabled:         true,
		ApplyToNew:      true,
	}
}

func TestNormalizeReturnRouteTemplateRejectsEmptyTarget(t *testing.T) {
	template := sampleMobileTemplate()
	template.Target = " "
	if err := normalizeReturnRouteTemplate(&template); err == nil {
		t.Fatal("expected empty target to fail")
	}
}

func TestApplyReturnRouteTemplatesCreatesNamedTasks(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	templates := []models.ReturnRouteTemplate{sampleMobileTemplate()}
	telecom := sampleMobileTemplate()
	telecom.Name = "XX电信 CN2 GIA"
	telecom.Carrier = "telecom"
	telecom.ExpectedLine = "CN2 GIA"
	telecom.Target = "202.96.209.133"
	templates = append(templates, telecom)
	if err := db.Create(&templates).Error; err != nil {
		t.Fatal(err)
	}

	result, created, err := applyReturnRouteTemplates(db, []uint{templates[0].Id, templates[1].Id}, []string{"node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 || result.Skipped != 0 || len(created) != 2 {
		t.Fatalf("apply result = %+v created=%d", result, len(created))
	}
	names := map[string]string{}
	for _, task := range created {
		names[task.Carrier] = task.Name
		if task.Client != "node-a" || task.MainlandReachabilityEnabled || task.MainlandReachabilityPingTaskID != nil {
			t.Fatalf("task = %#v", task)
		}
	}
	if names["mobile"] != "VMRack_LAX_L3_XX移动 CMIN2_V4" || names["telecom"] != "VMRack_LAX_L3_XX电信 CN2 GIA_V4" {
		t.Fatalf("task names = %#v", names)
	}
}

func TestApplyReturnRouteTemplatesAllowsDifferentExpectedLine(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	existing := taskFromReturnRouteTemplate(sampleMobileTemplate(), models.Client{UUID: "node-a", Name: "VMRack_LAX_L3"})
	existing.ExpectedLine = "CMNET"
	existing.Name = "VMRack_LAX_L3_XX移动 CMNET_V4"
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	template := sampleMobileTemplate()
	template.ExpectedLine = "CMIN2"
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}

	result, created, err := applyReturnRouteTemplates(db, []uint{template.Id}, []string{"node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 0 || len(created) != 1 {
		t.Fatalf("apply result = %+v created=%d", result, len(created))
	}
}

func TestTaskFromReturnRouteTemplateAlwaysEnabled(t *testing.T) {
	template := sampleMobileTemplate()
	template.Enabled = false
	task := taskFromReturnRouteTemplate(template, models.Client{UUID: "node-a", Name: "VMRack_LAX_L3"})
	if !task.Enabled {
		t.Fatal("template-created tasks must stay enabled")
	}
}

func TestNormalizeReturnRouteTemplateForcesEnabled(t *testing.T) {
	template := sampleMobileTemplate()
	template.Enabled = false
	if err := normalizeReturnRouteTemplate(&template); err != nil {
		t.Fatal(err)
	}
	if !template.Enabled {
		t.Fatal("templates do not have a disable switch")
	}
}

func TestApplyReturnRouteTemplatesSkipsExistingCarrierAndIPVersion(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	template := sampleMobileTemplate()
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}
	existing := taskFromReturnRouteTemplate(template, models.Client{UUID: "node-a", Name: "VMRack_LAX_L3"})
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}

	result, created, err := applyReturnRouteTemplates(db, []uint{template.Id}, []string{"node-a", "node-b"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 1 || len(created) != 1 {
		t.Fatalf("apply result = %+v created=%d", result, len(created))
	}
	if created[0].Client != "node-b" || created[0].Name != "HongKong-03_XX移动 CMIN2_V4" {
		t.Fatalf("created task = %#v", created[0])
	}
}

func TestApplyReturnRouteTemplatesNamesUseServerAndTemplate(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	template := sampleMobileTemplate()
	template.Name = "XX移动_CMIN2"
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}

	result, created, err := applyReturnRouteTemplates(db, []uint{template.Id}, []string{"node-a", "node-b"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 || result.Skipped != 0 || len(created) != 2 {
		t.Fatalf("apply result = %+v created=%d", result, len(created))
	}
	names := map[string]string{}
	for _, task := range created {
		names[task.Client] = task.Name
	}
	if names["node-a"] != "VMRack_LAX_L3_XX移动_CMIN2_V4" || names["node-b"] != "HongKong-03_XX移动_CMIN2_V4" {
		t.Fatalf("task names = %#v", names)
	}
}

func TestApplyReturnRouteTemplatesSkipsManualTaskWithSameProbe(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	existing := models.ReturnRouteTask{
		Name:            "manual-shanghai",
		Client:          "node-a",
		Carrier:         "mobile",
		Region:          "华东",
		Target:          "120.232.0.1",
		IPVersion:       4,
		ExpectedLine:    "CMIN2",
		Protocol:        "icmp",
		Interval:        180,
		SwitchConfirm:   2,
		RecoveryConfirm: 3,
		Cooldown:        1800,
		Notify:          true,
		NotifyRecovery:  true,
		Enabled:         true,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	template := sampleMobileTemplate()
	template.Name = "XX移动_CMIN2"
	template.ExpectedLine = "CMIN2"
	template.Target = "120.232.0.1"
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}

	result, created, err := applyReturnRouteTemplates(db, []uint{template.Id}, []string{"node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Skipped != 1 || len(created) != 0 {
		t.Fatalf("apply result = %+v created=%d", result, len(created))
	}
}

func TestApplyReturnRouteTemplatesCreatesTwoTargetsOnSameNode(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	beijing := sampleMobileTemplate()
	beijing.Name = "YY移动_CMIN2"
	beijing.Target = "120.232.0.1"
	beijing.ApplyToNew = true
	shanghai := sampleMobileTemplate()
	shanghai.Name = "XX移动_CMIN2"
	shanghai.Target = "221.130.33.52"
	shanghai.ApplyToNew = true
	templates := []models.ReturnRouteTemplate{beijing, shanghai}
	if err := db.Create(&templates).Error; err != nil {
		t.Fatal(err)
	}

	result, created, err := applyReturnRouteTemplates(db, []uint{templates[0].Id, templates[1].Id}, []string{"node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 || result.Skipped != 0 || len(created) != 2 {
		t.Fatalf("apply result = %+v created=%d", result, len(created))
	}
	names := map[string]string{}
	for _, task := range created {
		names[task.Target] = task.Name
	}
	if names["120.232.0.1"] != "VMRack_LAX_L3_YY移动_CMIN2_V4" || names["221.130.33.52"] != "VMRack_LAX_L3_XX移动_CMIN2_V4" {
		t.Fatalf("task names = %#v", names)
	}
}

func TestApplyReturnRouteTemplatesSkipsSecondApplyOfSameTemplate(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	template := sampleMobileTemplate()
	if err := db.Create(&template).Error; err != nil {
		t.Fatal(err)
	}

	first, created, err := applyReturnRouteTemplates(db, []uint{template.Id}, []string{"node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Created != 1 || first.Skipped != 0 || len(created) != 1 {
		t.Fatalf("first apply = %+v created=%d", first, len(created))
	}

	second, created, err := applyReturnRouteTemplates(db, []uint{template.Id}, []string{"node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != 0 || second.Skipped != 1 || len(created) != 0 {
		t.Fatalf("second apply = %+v created=%d", second, len(created))
	}
}

func TestReturnRouteTaskSlotTakenMatchesManualTaskAndUUIDCase(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	existing := models.ReturnRouteTask{
		Name:            "manual-mobile",
		Client:          "node-a",
		Carrier:         "Mobile",
		Region:          "华东",
		Target:          "120.232.0.1",
		IPVersion:       0,
		ExpectedLine:    "CMIN2",
		Protocol:        "icmp",
		Interval:        180,
		SwitchConfirm:   2,
		RecoveryConfirm: 3,
		Cooldown:        1800,
		Notify:          true,
		NotifyRecovery:  true,
		Enabled:         true,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	probe := models.ReturnRouteTask{Client: "NODE-A", Carrier: "mobile", Target: "120.232.0.1", IPVersion: 4, ExpectedLine: "CMIN2"}
	taken, err := returnRouteTaskSlotTaken(db, probe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !taken {
		t.Fatal("manual task should occupy the same probe slot")
	}
	if err := ensureReturnRouteTaskSlotFree(db, probe, nil); err == nil {
		t.Fatal("expected occupied slot to fail")
	} else if err.Error() != errReturnRouteTaskSlotTaken {
		t.Fatalf("error = %v", err)
	}
	if err := ensureReturnRouteTaskSlotFree(db, probe, []uint{existing.Id}); err != nil {
		t.Fatal(err)
	}
	otherTarget := probe
	otherTarget.Target = "221.130.33.52"
	if err := ensureReturnRouteTaskSlotFree(db, otherTarget, nil); err != nil {
		t.Fatal(err)
	}
}

func TestApplyReturnRouteTemplatesIPv6NameSuffix(t *testing.T) {
	if got := returnRouteTaskNameForClient("Tokyo", "联通9929", 6); got != "Tokyo_联通9929_V6" {
		t.Fatalf("ipv6 name = %q", got)
	}
	if got := returnRouteTaskNameForClient("Tokyo", "联通9929", 4); got != "Tokyo_联通9929_V4" {
		t.Fatalf("ipv4 name = %q", got)
	}
}

func TestApplyReturnRouteTemplatesAllowsSameCarrierDifferentIPVersion(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	v4 := sampleMobileTemplate()
	v6 := sampleMobileTemplate()
	v6.Name = "XX移动 CMIN2 IPv6"
	v6.IPVersion = 6
	v6.Target = "2409:8080::1"
	templates := []models.ReturnRouteTemplate{v4, v6}
	if err := db.Create(&templates).Error; err != nil {
		t.Fatal(err)
	}

	result, created, err := applyReturnRouteTemplates(db, []uint{templates[0].Id, templates[1].Id}, []string{"node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 || result.Skipped != 0 || len(created) != 2 {
		t.Fatalf("apply result = %+v created=%d", result, len(created))
	}
	names := map[int]string{}
	for _, task := range created {
		names[task.IPVersion] = task.Name
	}
	if names[4] != "VMRack_LAX_L3_XX移动 CMIN2_V4" || names[6] != "VMRack_LAX_L3_XX移动 CMIN2 IPv6_V6" {
		t.Fatalf("task names = %#v", names)
	}
}

func TestApplyReturnRouteTemplatesRequiresIDsAndClients(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	if _, _, err := applyReturnRouteTemplates(db, nil, []string{"node-a"}); err == nil {
		t.Fatal("expected template ids error")
	}
	if _, _, err := applyReturnRouteTemplates(db, []uint{1}, nil); err == nil {
		t.Fatal("expected clients error")
	}
}

func TestReturnRouteTemplatesAllowSameCarrierLineDifferentTarget(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	beijing := sampleMobileTemplate()
	beijing.Name = "YY移动 CMIN2"
	beijing.Target = "120.232.0.1"
	beijing.ApplyToNew = false
	if err := db.Create(&beijing).Error; err != nil {
		t.Fatal(err)
	}
	shanghai := sampleMobileTemplate()
	shanghai.Name = "XX移动 CMIN2"
	shanghai.Target = "221.130.33.52"
	shanghai.ApplyToNew = false
	if err := ensureReturnRouteTemplateConstraints(db, &shanghai); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&shanghai).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEnsureReturnRouteTemplateNameUniqueRejectsDuplicate(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	existing := sampleMobileTemplate()
	existing.ApplyToNew = false
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	dup := sampleMobileTemplate()
	dup.Target = "221.130.33.52"
	dup.ExpectedLine = "CMNET"
	dup.ApplyToNew = false
	if err := ensureReturnRouteTemplateNameUnique(db, &dup); err == nil {
		t.Fatal("expected duplicate name to fail")
	}
	if err := ensureReturnRouteTemplateConstraints(db, &dup); err == nil {
		t.Fatal("expected constraints to reject duplicate name")
	}
}

func TestEnsureReturnRouteTemplateNameUniqueAllowsEditingSelf(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	existing := sampleMobileTemplate()
	existing.ApplyToNew = false
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureReturnRouteTemplateNameUnique(db, &existing); err != nil {
		t.Fatal(err)
	}
	if err := ensureReturnRouteTemplateConstraints(db, &existing); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureReturnRouteTemplateNameUniqueIsCaseInsensitive(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	existing := sampleMobileTemplate()
	existing.Name = "East China Mobile"
	existing.ApplyToNew = false
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	dup := sampleMobileTemplate()
	dup.Name = "east china mobile"
	dup.Target = "1.1.1.1"
	dup.ApplyToNew = false
	if err := ensureReturnRouteTemplateNameUnique(db, &dup); err == nil {
		t.Fatal("expected case-insensitive duplicate name to fail")
	}
}

func TestEnsureReturnRouteTemplateAllowsMultipleApplyToNewSameCarrier(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	existing := sampleMobileTemplate()
	existing.ApplyToNew = true
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	other := sampleMobileTemplate()
	other.Name = "XX移动 CMIN2"
	other.Target = "221.130.33.52"
	other.ApplyToNew = true
	if err := ensureReturnRouteTemplateConstraints(db, &other); err != nil {
		t.Fatal(err)
	}
}

func TestEditReturnRouteTemplatesBatchUpdatesSharedFields(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	mobile := sampleMobileTemplate()
	mobile.ApplyToNew = false
	telecom := sampleMobileTemplate()
	telecom.Name = "XX电信 CN2 GIA"
	telecom.Carrier = "telecom"
	telecom.ExpectedLine = "CN2 GIA"
	telecom.Target = "202.96.209.133"
	telecom.ApplyToNew = false
	templates := []models.ReturnRouteTemplate{mobile, telecom}
	if err := db.Create(&templates).Error; err != nil {
		t.Fatal(err)
	}

	err := editReturnRouteTemplatesBatch(db, ReturnRouteTemplateBatchEdit{
		IDs:             []uint{templates[0].Id, templates[1].Id},
		Region:          "华南",
		Target:          "1.2.3.4",
		Interval:        300,
		SwitchConfirm:   4,
		RecoveryConfirm: 5,
		Cooldown:        600,
		Notify:          false,
		NotifyRecovery:  false,
		ApplyToNew:      false,
	})
	if err != nil {
		t.Fatal(err)
	}

	var updated []models.ReturnRouteTemplate
	if err := db.Order("id ASC").Find(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 {
		t.Fatalf("updated count = %d", len(updated))
	}
	for _, template := range updated {
		if template.Region != "华南" || template.Target != "1.2.3.4" || template.Interval != 300 || !template.Enabled || template.ApplyToNew {
			t.Fatalf("template = %#v", template)
		}
	}
	if updated[0].Carrier != "mobile" || updated[0].ExpectedLine != "CMIN2" {
		t.Fatalf("mobile identity changed: %#v", updated[0])
	}
	if updated[1].Carrier != "telecom" || updated[1].ExpectedLine != "CN2 GIA" {
		t.Fatalf("telecom identity changed: %#v", updated[1])
	}
}

func TestEditReturnRouteTemplatesBatchAllowsMultipleApplyToNew(t *testing.T) {
	db := newReturnRouteTemplateTestDB(t)
	first := sampleMobileTemplate()
	second := sampleMobileTemplate()
	second.Name = "XX移动 CMNET"
	second.ExpectedLine = "CMNET"
	second.ApplyToNew = false
	templates := []models.ReturnRouteTemplate{first, second}
	if err := db.Create(&templates).Error; err != nil {
		t.Fatal(err)
	}

	err := editReturnRouteTemplatesBatch(db, ReturnRouteTemplateBatchEdit{
		IDs:             []uint{templates[0].Id, templates[1].Id},
		Region:          "华东",
		Target:          "120.232.0.1",
		Interval:        180,
		SwitchConfirm:   2,
		RecoveryConfirm: 3,
		Cooldown:        1800,
		Notify:          true,
		NotifyRecovery:  true,
		ApplyToNew:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var updated []models.ReturnRouteTemplate
	if err := db.Order("id ASC").Find(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || !updated[0].ApplyToNew || !updated[1].ApplyToNew {
		t.Fatalf("updated = %#v", updated)
	}
}
