package jsonrpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/billing"
	"github.com/nuomiiiii/lite/database/models"
)

func sampleThemeClient(hidden bool, token string) models.Client {
	day := 4
	expired := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	return models.Client{
		UUID:                   "node-1",
		Token:                  token,
		Name:                   "edge",
		CpuName:                "cpu",
		Virtualization:         "kvm",
		Arch:                   "amd64",
		CpuCores:               4,
		CpuPhysicalCores:       2,
		OS:                     "linux",
		KernelVersion:          "6.1",
		GpuName:                "none",
		IPv4:                   "1.2.3.4",
		IPv6:                   "2001:db8::1",
		Region:                 "HK",
		RegionOverride:         "",
		Remark:                 "private",
		PublicRemark:           "public",
		MemTotal:               1024,
		SwapTotal:              512,
		DiskTotal:              2048,
		Version:                "agent-9",
		Weight:                 1,
		Price:                  10,
		BillingCycle:           30,
		AutoRenewal:            true,
		Currency:               "USD",
		ExpiredAt:              &expired,
		Group:                  "prod",
		Tags:                   "tag",
		Bandwidth:              "1G",
		Hidden:                 hidden,
		RemoteControlProtected: true,
		TrafficLimit:           100,
		TrafficLimitType:       "sum",
		TrafficResetDay:        &day,
		TrafficResetAllowance:  50,
		TrafficResetCycle:      "month",
		EffectiveTrafficLimit:  80,
		EffectiveTrafficType:   "max",
		DeploymentStatus:       "applied",
		CreatedAt:              time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:              time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

func jsonObjectKeys(t *testing.T, value any) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys := make(map[string]bool, len(object))
	for key := range object {
		keys[key] = true
	}
	return keys
}

func TestThemeNodeOmitsForbiddenFieldsForGuestAndAdmin(t *testing.T) {
	nodes := []models.Client{sampleThemeClient(false, "secret-token")}
	for _, isAdmin := range []bool{false, true} {
		presented := presentThemeNodes(nodes, isAdmin, true)
		if len(presented) != 1 {
			t.Fatalf("admin=%v: expected 1 node, got %d", isAdmin, len(presented))
		}
		keys := jsonObjectKeys(t, presented[0])
		for _, forbidden := range themeNodeForbiddenJSONKeys {
			if keys[forbidden] {
				t.Fatalf("admin=%v: forbidden field %q is present", isAdmin, forbidden)
			}
		}
		if !keys["kernel_version"] {
			t.Fatalf("admin=%v: kernel_version should remain", isAdmin)
		}
		if !keys["uuid"] || !keys["effective_traffic_limit"] || !keys["traffic_reset_day"] || !keys["traffic_reset_at"] || !keys["expiry_timezone"] {
			t.Fatalf("admin=%v: required theme fields missing: %v", isAdmin, keys)
		}
		if presented[0].TrafficResetAt == "" || presented[0].TrafficResetTimezone != "Asia/Shanghai" {
			t.Fatalf("admin=%v: traffic reset datetime missing: %+v", isAdmin, presented[0])
		}
		if presented[0].ExpiryTimezone != "Asia/Shanghai" {
			t.Fatalf("admin=%v: empty expiry timezone = %q, want Asia/Shanghai", isAdmin, presented[0].ExpiryTimezone)
		}
	}
	named := sampleThemeClient(false, "secret-token")
	named.ExpiryTimezone = "America/New_York"
	presented := presentThemeNodes([]models.Client{named}, false, true)
	if presented[0].ExpiryTimezone != "America/New_York" {
		t.Fatalf("named expiry timezone = %q", presented[0].ExpiryTimezone)
	}
}

func TestThemeNodeMapAndSingleObjectOmitForbiddenFields(t *testing.T) {
	nodes := presentThemeNodes([]models.Client{sampleThemeClient(false, "secret-token")}, true, true)
	singleKeys := jsonObjectKeys(t, nodes[0])
	mapKeys := jsonObjectKeys(t, themeNodeMap(nodes)["node-1"])
	for _, keys := range []map[string]bool{singleKeys, mapKeys} {
		for _, forbidden := range themeNodeForbiddenJSONKeys {
			if keys[forbidden] {
				t.Fatalf("forbidden field %q leaked in getNodes DTO", forbidden)
			}
		}
	}
}

func TestPresentThemeNodesHidesGuestNodesAndMasksIPs(t *testing.T) {
	nodes := []models.Client{
		sampleThemeClient(true, "secret"),
		sampleThemeClient(false, "secret"),
	}
	nodes[1].UUID = "node-visible"
	nodes[1].IPv4 = "8.8.4.4"
	nodes[1].IPv6 = "fe80::1"

	guestHidden := presentThemeNodes(nodes, false, false)
	if len(guestHidden) != 1 || guestHidden[0].UUID != "node-visible" {
		t.Fatalf("guest should only see visible nodes: %+v", guestHidden)
	}
	if guestHidden[0].IPv4 != "" || guestHidden[0].IPv6 != "" {
		t.Fatalf("guest hidden-IP mode should omit addresses: %+v", guestHidden[0])
	}

	guestMasked := presentThemeNodes(nodes, false, true)
	if guestMasked[0].IPv4 != "8.*.*.*" {
		t.Fatalf("unexpected masked ipv4: %q", guestMasked[0].IPv4)
	}
	if guestMasked[0].IPv6 != "fe80:*:*:*:*:*:*:*" {
		t.Fatalf("unexpected masked ipv6: %q", guestMasked[0].IPv6)
	}

	admin := presentThemeNodes(nodes, true, false)
	if len(admin) != 2 {
		t.Fatalf("admin should see hidden nodes, got %d", len(admin))
	}
	hidden, ok := themeNodeByUUID(admin, "node-1")
	if !ok || !hidden.Hidden || hidden.IPv4 != "1.2.3.4" || hidden.IPv6 != "2001:db8::1" {
		t.Fatalf("admin should receive full IPs for hidden nodes: %+v", hidden)
	}
}

func TestPresentThemeNodesAppliesTrafficCompatibility(t *testing.T) {
	nodes := []models.Client{sampleThemeClient(false, "secret")}
	presented := presentThemeNodes(nodes, true, true)
	if presented[0].TrafficLimit != 80 || presented[0].TrafficLimitType != "max" {
		t.Fatalf("theme traffic compatibility not applied: %+v", presented[0])
	}
	if presented[0].EffectiveTrafficLimit != 80 || presented[0].EffectiveTrafficType != "max" {
		t.Fatalf("effective quota missing: %+v", presented[0])
	}
}

func TestThemeNodeTrafficResetAtUsesConfiguredClockAndTimezone(t *testing.T) {
	node := sampleThemeClient(false, "secret")
	day := 15
	node.TrafficResetDay = &day
	node.TrafficResetTime = "12:38:12"
	node.TrafficResetTimezone = "UTC"
	presented := presentThemeNodes([]models.Client{node}, false, true)
	if presented[0].TrafficResetTime != "12:38:12" || presented[0].TrafficResetTimezone != "UTC" {
		t.Fatalf("theme reset clock = %s %s", presented[0].TrafficResetTime, presented[0].TrafficResetTimezone)
	}
	if !strings.Contains(presented[0].TrafficResetAt, "T12:38:12Z") {
		t.Fatalf("traffic_reset_at = %q, want the UTC instant 12:38:12", presented[0].TrafficResetAt)
	}

	node.TrafficResetTime = "00:00:00"
	node.TrafficResetTimezone = "Asia/Shanghai"
	presented = presentThemeNodes([]models.Client{node}, false, true)
	if presented[0].TrafficResetTimezone != "Asia/Shanghai" {
		t.Fatalf("shanghai timezone = %q", presented[0].TrafficResetTimezone)
	}
	if !strings.Contains(presented[0].TrafficResetAt, "T00:00:00+08:00") {
		t.Fatalf("shanghai traffic_reset_at = %q, want 00:00:00+08:00", presented[0].TrafficResetAt)
	}
}

func TestThemeNodeTrafficResetDayIsBeijingDayOfNextReset(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	day := 15
	node := sampleThemeClient(false, "secret")
	node.TrafficResetDay = &day
	node.TrafficResetTime = "12:38:12"
	node.TrafficResetTimezone = "UTC"
	sameDay := themeTrafficResetDay(node, now)
	if sameDay == nil || *sameDay != 15 {
		t.Fatalf("UTC 15 12:38:12 should stay Beijing day 15, got %v", sameDay)
	}

	node.TrafficResetTime = "20:00:00"
	crossed := themeTrafficResetDay(node, now)
	if crossed == nil || *crossed != 16 {
		t.Fatalf("UTC 15 20:00:00 should become Beijing day 16, got %v", crossed)
	}

	node.TrafficResetTime = "00:00:00"
	node.TrafficResetTimezone = "Asia/Shanghai"
	local := themeTrafficResetDay(node, now)
	if local == nil || *local != 15 {
		t.Fatalf("Beijing 15 00:00 should stay day 15, got %v", local)
	}
}

func TestToThemeNodeEmptyResetClockDoesNotPanic(t *testing.T) {
	day := 21
	node := sampleThemeClient(false, "secret-token")
	node.TrafficResetDay = &day
	node.TrafficResetTime = ""
	node.TrafficResetTimezone = ""
	var got ThemeNode
	requireNotPanic := func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("toThemeNode panicked: %v", recovered)
			}
		}()
		got = toThemeNode(node)
	}
	requireNotPanic()
	if got.TrafficResetDay == nil || *got.TrafficResetDay != 21 {
		t.Fatalf("empty clock should still expose reset day 21, got %v", got.TrafficResetDay)
	}
}

func TestApplyThemeRemainingValuesAttachesNativeAmount(t *testing.T) {
	nodes := presentThemeNodes([]models.Client{sampleThemeClient(false, "secret-token")}, false, true)
	if nodes[0].RemainingValue != nil || nodes[0].RemainingValueCurrency != "" {
		t.Fatalf("remaining value should stay omitted until attached: %+v", nodes[0])
	}
	applyThemeRemainingValues(nodes, map[string]billing.RemainingAmount{
		"node-1": {Value: "12.340000", Currency: "USD"},
		"other":  {Value: "1.000000", Currency: "CNY"},
	})
	if nodes[0].RemainingValue == nil || *nodes[0].RemainingValue != "12.340000" {
		t.Fatalf("remaining value = %v", nodes[0].RemainingValue)
	}
	if nodes[0].RemainingValueCurrency != "USD" {
		t.Fatalf("remaining currency = %q", nodes[0].RemainingValueCurrency)
	}
	keys := jsonObjectKeys(t, nodes[0])
	if !keys["remaining_value"] || !keys["remaining_value_currency"] {
		t.Fatalf("remaining fields missing: %v", keys)
	}
	mapped := themeNodeMap(nodes)["node-1"]
	if mapped.RemainingValue == nil || *mapped.RemainingValue != "12.340000" || mapped.RemainingValueCurrency != "USD" {
		t.Fatalf("map copy dropped remaining value: %+v", mapped)
	}
}

func TestApplyThemeRemainingValuesOmitsMissingAndEmpty(t *testing.T) {
	nodes := presentThemeNodes([]models.Client{sampleThemeClient(false, "secret-token")}, false, true)
	applyThemeRemainingValues(nodes, map[string]billing.RemainingAmount{
		"node-1": {Value: "", Currency: "USD"},
	})
	if nodes[0].RemainingValue != nil {
		t.Fatalf("empty remaining value should stay omitted: %+v", nodes[0])
	}
	keys := jsonObjectKeys(t, nodes[0])
	if keys["remaining_value"] || keys["remaining_value_currency"] {
		t.Fatalf("empty remaining value leaked: %v", keys)
	}
}

func TestRPCClientsWithoutMCPOmitCapabilityFields(t *testing.T) {
	node := sampleThemeClient(false, "secret-token")
	node.MCPFull = true
	node.MCPFullVersion = 1
	keys := jsonObjectKeys(t, rpcClientsWithoutMCP([]models.Client{node})[0])
	for _, forbidden := range []string{"mcp_full", "mcp_full_version"} {
		if keys[forbidden] {
			t.Fatalf("RPC client leaked %q", forbidden)
		}
	}
	if !keys["name"] || !keys["uuid"] {
		t.Fatalf("RPC client missing public fields: %v", keys)
	}
}
