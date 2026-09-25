package jsonrpc

import (
	"context"
	"testing"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
)

func TestNormalizeAdminDefaultPageSize(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int
		ok    bool
	}{
		{name: "minimum", value: float64(5), want: 5, ok: true},
		{name: "custom", value: float64(40), want: 40, ok: true},
		{name: "maximum", value: float64(100), want: 100, ok: true},
		{name: "below minimum", value: float64(4), ok: false},
		{name: "above maximum", value: float64(101), ok: false},
		{name: "fraction", value: 10.5, ok: false},
		{name: "wrong type", value: "30", ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := normalizeAdminDefaultPageSize(test.value)
			if got != test.want || ok != test.ok {
				t.Fatalf("normalizeAdminDefaultPageSize(%v) = (%d, %v), want (%d, %v)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestStripRetiredAdminSettingsRemovesAutoDiscoveryKey(t *testing.T) {
	settings := map[string]any{
		config.SitenameKey:                       "Lite",
		config.AutoDiscoveryKeyKey:               "leftover-key",
		config.CloudflareTunnelTokenKey:          "tunnel",
		config.LowResourceModeKey:                true,
		config.SiteFactoryDefaultsKey:            true,
		metricstore.MetricDownsamplingEnabledKey: true,
	}
	stripRetiredAdminSettings(settings)
	if _, ok := settings[config.AutoDiscoveryKeyKey]; ok {
		t.Fatal("admin settings must not return auto_discovery_key")
	}
	if _, ok := settings[config.SitenameKey]; !ok {
		t.Fatal("unrelated settings must stay")
	}
	for _, key := range []string{
		config.CloudflareTunnelTokenKey,
		config.LowResourceModeKey,
		config.SiteFactoryDefaultsKey,
		metricstore.MetricDownsamplingEnabledKey,
	} {
		if _, ok := settings[key]; ok {
			t.Fatalf("%s should be stripped", key)
		}
	}
}

func TestSettingsRequireHumanSessionIncludesLoginKeys(t *testing.T) {
	for _, key := range []string{
		config.CustomHeadKey,
		config.CustomBodyKey,
		config.ThemeKey,
		config.DisablePasswordLoginKey,
		config.OAuthEnabledKey,
		config.OAuthProviderKey,
	} {
		if !settingsRequireHumanSession(map[string]any{key: true}) {
			t.Fatalf("%s must require an administrator session", key)
		}
	}
	if settingsRequireHumanSession(map[string]any{config.SitenameKey: "Lite"}) {
		t.Fatal("unrelated settings must still be writable with an API key")
	}
}

func TestAdminEditSettingsRejectsAPIKeyForLoginKeys(t *testing.T) {
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		Principal:  rpc.NewAPIKeyPrincipal(),
		Permission: rpc.RoleAdmin,
	})
	_, rpcErr := adminEditSettings(ctx, &rpc.JsonRpcRequest{Version: rpc.RPC_VERSION, ID: 1, Method: "admin:editSettings", Params: map[string]any{
		config.DisablePasswordLoginKey: true,
	}})
	if rpcErr == nil || rpcErr.Code != rpc.PermissionDenied {
		t.Fatalf("API key password-login change error = %#v", rpcErr)
	}
}

func TestAdminSetOidcRejectsAPIKey(t *testing.T) {
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		Principal:  rpc.NewAPIKeyPrincipal(),
		Permission: rpc.RoleAdmin,
	})
	_, rpcErr := adminSetOidc(ctx, &rpc.JsonRpcRequest{Version: rpc.RPC_VERSION, ID: 1, Method: "admin:setOidcProvider", Params: map[string]any{
		"name": "github",
	}})
	if rpcErr == nil || rpcErr.Code != rpc.PermissionDenied {
		t.Fatalf("API key OIDC change error = %#v", rpcErr)
	}
}
