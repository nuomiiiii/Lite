package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nuomiiiii/lite/database/models"
)

func dualStackNode() models.Client {
	return models.Client{
		UUID:                 "11111111-2222-3333-4444-555555555555",
		Name:                 "hk-01",
		OS:                   "linux",
		Arch:                 "amd64",
		IPv4:                 "203.0.113.7",
		IPv6:                 "2001:db8::7",
		MCPFull:              true,
		MCPFullVersion:       3,
		RemoteControlEnabled: true,
	}
}

func TestAgentSummaryCarriesBothAddressFamilies(t *testing.T) {
	node := dualStackNode()
	summary := agentSummary(node)
	if summary["ipv4"] != node.IPv4 {
		t.Fatalf("ipv4 = %v, want %q", summary["ipv4"], node.IPv4)
	}
	if summary["ipv6"] != node.IPv6 {
		t.Fatalf("ipv6 = %v, want %q", summary["ipv6"], node.IPv6)
	}

	// The MCP client reads JSON, so assert the fields survive serialization.
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if decoded["ipv4"] != node.IPv4 || decoded["ipv6"] != node.IPv6 {
		t.Fatalf("serialized summary = %s", encoded)
	}
}

func TestAgentSummaryKeepsEmptyAddressKeys(t *testing.T) {
	// A node with no recorded IPv6 keeps an explicit empty ipv6 instead of
	// dropping the key, so clients can distinguish "no recorded IPv6" from a
	// missing field.
	summary := agentSummary(models.Client{UUID: "ipv4-only", IPv4: "198.51.100.9"})
	if summary["ipv4"] != "198.51.100.9" {
		t.Fatalf("ipv4 = %v", summary["ipv4"])
	}
	if value, ok := summary["ipv6"]; !ok || value != "" {
		t.Fatalf("ipv6 = %#v, want present and empty", value)
	}
}

func TestAgentMatchesQueryCoversNamesAndAddresses(t *testing.T) {
	node := dualStackNode()
	cases := []struct {
		query string
		want  bool
	}{
		{"", true},
		{"hk-01", true},
		{"HK-01", true},
		{"11111111-2222", true},
		{"linux", true},
		{"203.0.113.7", true},
		{"203.0.113", true},
		{"2001:db8::7", true},
		{"2001:DB8::7", true},
		{"203.0.113.8", false},
		{"2001:db9::7", false},
	}
	for _, testCase := range cases {
		if got := agentMatchesQuery(node, testCase.query); got != testCase.want {
			t.Fatalf("agentMatchesQuery(%q) = %t, want %t", testCase.query, got, testCase.want)
		}
	}
}

func TestAgentMatchesQueryIgnoresNodesWithoutThatAddress(t *testing.T) {
	// A node without addresses must not match an address query.
	node := models.Client{UUID: "bare", Name: "bare"}
	if agentMatchesQuery(node, "203.0.113.7") {
		t.Fatal("address-less node should not match an address query")
	}
	if !agentMatchesQuery(node, "bare") {
		t.Fatal("address-less node should still match its own name")
	}
}

func TestListAgentsToolDescriptionAnnouncesAddresses(t *testing.T) {
	found := false
	for _, descriptor := range toolDescriptors() {
		if descriptor["name"] != "list_agents" {
			continue
		}
		found = true
		description, _ := descriptor["description"].(string)
		lowered := strings.ToLower(description)
		if !strings.Contains(lowered, "ipv4") || !strings.Contains(lowered, "ipv6") {
			t.Fatalf("list_agents description = %q", description)
		}
	}
	if !found {
		t.Fatal("list_agents tool descriptor is missing")
	}
}
