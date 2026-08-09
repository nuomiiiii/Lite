package public

import (
	"testing"
)

func TestThemeNavigationBuildsSafeServerAndTaskURL(t *testing.T) {
	navigation, ok := parseThemeNavigation([]byte(`{"navigation":{"server_detail":"/server/{uuid}","server_network":"/server/{uuid}?view=network","ping_task_parameter":"ping_task"}}`))
	if !ok {
		t.Fatal("valid theme navigation was rejected")
	}
	if got := navigation.ServerDetailURL("node/a", 7); got != "/server/node%2Fa?ping_task=7" {
		t.Fatalf("server detail URL = %q", got)
	}
	if got := navigation.ServerNetworkURL("node/a"); got != "/server/node%2Fa?view=network" {
		t.Fatalf("server network URL = %q", got)
	}
}

func TestThemeNavigationRejectsExternalAndTraversalRoutes(t *testing.T) {
	for _, route := range []string{
		"https://example.com/server/{uuid}",
		"/server/../{uuid}",
		"//example.com/{uuid}",
		"/server/static",
		"/server/{id}",
		"/server/{uuid}/{id}",
	} {
		manifest := []byte(`{"navigation":{"server_detail":"` + route + `"}}`)
		if _, ok := parseThemeNavigation(manifest); ok {
			t.Fatalf("unsafe route %q was accepted", route)
		}
	}
}

func TestBundledThemeNavigationKeepsNezhaAndLegacyFallbackRoutes(t *testing.T) {
	if got := bundledThemeNavigation(DefaultTheme).ServerDetailURL("node-a", 9); got != "/server/node-a?ping_task=9" {
		t.Fatalf("Nezha detail URL = %q", got)
	}
	if got := bundledThemeNavigation(DefaultTheme).ServerNetworkURL("node-a"); got != "/server/node-a?view=network" {
		t.Fatalf("Nezha network URL = %q", got)
	}
	if got := bundledThemeNavigation("unknown").ServerDetailURL("node-a", 9); got != "/instance/node-a" {
		t.Fatalf("legacy third-party theme detail URL = %q", got)
	}
}

func TestThemeNavigationFallsBackToServerDetailsWithoutNetworkRoute(t *testing.T) {
	navigation, ok := parseThemeNavigation([]byte(`{"navigation":{"server_detail":"/nodes/{uuid}","ping_task_parameter":"task"}}`))
	if !ok {
		t.Fatal("valid legacy theme navigation was rejected")
	}
	if got := navigation.ServerNetworkURL("node-a"); got != "/nodes/node-a" {
		t.Fatalf("legacy network fallback URL = %q", got)
	}
}
