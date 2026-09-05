package jsonrpc

import (
	"encoding/json"
	"testing"
	"time"

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
		if !keys["uuid"] || !keys["effective_traffic_limit"] || !keys["traffic_reset_day"] {
			t.Fatalf("admin=%v: required theme fields missing: %v", isAdmin, keys)
		}
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
