package public

import (
	"testing"
)

func TestThemeNavigationBuildsSafeServerAndTaskURL(t *testing.T) {
	navigation, ok := parseThemeNavigation([]byte(`{"navigation":{"server_detail":"/server/{uuid}","ping_task_parameter":"ping_task"}}`))
	if !ok {
		t.Fatal("valid theme navigation was rejected")
	}
	if got := navigation.ServerDetailURL("node/a", 7); got != "/server/node%2Fa?ping_task=7" {
		t.Fatalf("server detail URL = %q", got)
	}
}

func TestThemeNavigationSupportsLegacyNumericServerRoute(t *testing.T) {
	navigation, ok := parseThemeNavigation([]byte(`{"navigation":{"server_detail":"/server/{id}"}}`))
	if !ok {
		t.Fatal("valid numeric theme navigation was rejected")
	}
	if got := navigation.ServerDetailURL("node-a", 0); got != "/server/3254795094" {
		t.Fatalf("numeric server detail URL = %q", got)
	}
}

func TestThemeNavigationRejectsExternalAndTraversalRoutes(t *testing.T) {
	for _, route := range []string{
		"https://example.com/server/{uuid}",
		"/server/../{uuid}",
		"//example.com/{uuid}",
		"/server/static",
		"/server/{uuid}/{id}",
	} {
		manifest := []byte(`{"navigation":{"server_detail":"` + route + `"}}`)
		if _, ok := parseThemeNavigation(manifest); ok {
			t.Fatalf("unsafe route %q was accepted", route)
		}
	}
}

func TestBundledThemeNavigationKeepsBothDetailRoutes(t *testing.T) {
	if got := bundledThemeNavigation(DefaultTheme).ServerDetailURL("node-a", 9); got != "/server/node-a?ping_task=9" {
		t.Fatalf("Nezha detail URL = %q", got)
	}
	if got := bundledThemeNavigation(ClassicTheme).ServerDetailURL("node-a", 9); got != "/instance/node-a" {
		t.Fatalf("classic detail URL = %q", got)
	}
	if got := bundledThemeNavigation("unknown").ServerDetailURL("node-a", 9); got != "/instance/node-a" {
		t.Fatalf("legacy third-party theme detail URL = %q", got)
	}
}
