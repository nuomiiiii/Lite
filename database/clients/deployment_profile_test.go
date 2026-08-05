package clients

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/komari-monitor/komari/database/models"
	v2 "github.com/komari-monitor/komari/protocol/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDeploymentProfileRuntimeConfigExcludesInstallationOnlyFields(t *testing.T) {
	profile := DeploymentProfile{
		Platform:                "linux",
		DisableWebSSH:           true,
		DisableAutoUpdate:       true,
		IgnoreUnsafeCert:        true,
		GetIPAddrFromNIC:        true,
		EnableGHProxy:           true,
		GHProxy:                 "https://example.com",
		EnableCustomDir:         true,
		Dir:                     "/opt/custom-agent",
		EnableCustomServiceName: true,
		ServiceName:             "custom-agent",
		EnableInterval:          true,
		Interval:                15,
	}
	if err := normalizeDeploymentProfile(&profile); err != nil {
		t.Fatalf("normalizeDeploymentProfile() error = %v", err)
	}
	encoded, err := json.Marshal(profile.RuntimeConfig())
	if err != nil {
		t.Fatalf("marshal runtime config: %v", err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{
		"disable_web_ssh",
		"disable_auto_update",
		"ignore_unsafe_cert",
		"get_ip_addr_from_nic",
		"ghproxy",
		"dir",
		"service_name",
	} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("runtime config contains installation-only field %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(payload, `"interval":15`) {
		t.Fatalf("runtime config is missing interval: %s", payload)
	}
}

func TestNormalizeDeploymentProfileRejectsInvalidRuntimeValues(t *testing.T) {
	profile := DeploymentProfile{
		Platform:       "linux",
		EnableInterval: true,
		Interval:       0.5,
	}
	if err := normalizeDeploymentProfile(&profile); err == nil {
		t.Fatal("expected invalid interval to be rejected")
	}

	profile = DeploymentProfile{
		Platform:          "linux",
		EnableMonthRotate: true,
		MonthRotate:       32,
	}
	if err := normalizeDeploymentProfile(&profile); err == nil {
		t.Fatal("expected invalid month rotation day to be rejected")
	}
}

func TestDeploymentProfileAndBillingEditorShareTrafficResetDay(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:deployment-profile-reset-day?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := db.Create(&models.Client{UUID: "node-a", Token: "token-a"}).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}

	profile := DeploymentProfile{
		Platform:          "linux",
		EnableMonthRotate: true,
		MonthRotate:       17,
	}
	if _, err := saveDeploymentProfile(db, "node-a", profile); err != nil {
		t.Fatalf("save deployment profile: %v", err)
	}
	var client models.Client
	if err := db.Select("uuid", "traffic_reset_day").First(&client, "uuid = ?", "node-a").Error; err != nil {
		t.Fatalf("read client: %v", err)
	}
	if client.TrafficResetDay == nil || *client.TrafficResetDay != 17 {
		t.Fatalf("billing reset day = %v, want 17", client.TrafficResetDay)
	}

	if err := saveClient(db, map[string]interface{}{
		"uuid":              "node-a",
		"traffic_reset_day": float64(9),
	}); err != nil {
		t.Fatalf("save billing reset day: %v", err)
	}
	loaded, saved, err := getDeploymentProfile(db, "node-a")
	if err != nil {
		t.Fatalf("read deployment profile: %v", err)
	}
	if !saved || !loaded.EnableMonthRotate || loaded.MonthRotate != 9 {
		t.Fatalf("deployment reset day = enabled:%v day:%d, want enabled:true day:9", loaded.EnableMonthRotate, loaded.MonthRotate)
	}
}

func TestAdoptDeploymentRuntimeConfigOnlyInitializesUnmanagedNode(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:deployment-profile-agent-state?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.Client{}, &models.ClientDeploymentProfile{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if err := db.Create(&models.Client{UUID: "node-state", Token: "token-state"}).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}

	day, interval := 8, 12.0
	include, exclude, mounts := "eth0", "lo", "/;/data"
	memoryCache, gpu := true, true
	adopted, err := adoptDeploymentRuntimeConfig(db, "node-state", "linux", v2.ConfigParams{
		MonthRotate:        &day,
		Interval:           &interval,
		IncludeNics:        &include,
		ExcludeNics:        &exclude,
		IncludeMountpoints: &mounts,
		MemoryIncludeCache: &memoryCache,
		EnableGPU:          &gpu,
	})
	if err != nil {
		t.Fatalf("adopt runtime config: %v", err)
	}
	if !adopted {
		t.Fatal("first runtime config was not adopted")
	}
	profile, saved, err := getDeploymentProfile(db, "node-state")
	if err != nil {
		t.Fatalf("load adopted profile: %v", err)
	}
	if !saved || !profile.EnableMonthRotate || profile.MonthRotate != day ||
		!profile.EnableInterval || profile.Interval != interval ||
		!profile.EnableIncludeNics || profile.IncludeNics != include ||
		!profile.EnableExcludeNics || profile.ExcludeNics != exclude ||
		!profile.EnableIncludeMountpoints || profile.IncludeMountpoints != mounts ||
		!profile.MemoryIncludeCache || !profile.EnableGPU {
		t.Fatalf("incomplete adopted profile: %+v", profile)
	}

	staleInterval := 5.0
	adopted, err = adoptDeploymentRuntimeConfig(db, "node-state", "windows", v2.ConfigParams{Interval: &staleInterval})
	if err != nil {
		t.Fatalf("submit stale runtime config: %v", err)
	}
	if adopted {
		t.Fatal("stale runtime config overwrote a managed profile")
	}
	profile, _, err = getDeploymentProfile(db, "node-state")
	if err != nil {
		t.Fatalf("reload managed profile: %v", err)
	}
	if profile.Platform != "linux" || profile.Interval != interval {
		t.Fatalf("managed profile changed after stale report: %+v", profile)
	}
}
