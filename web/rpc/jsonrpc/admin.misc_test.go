package jsonrpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nuomiiiii/lite/cmd/flags"
	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"github.com/nuomiiiii/lite/utils/instancekey"
	"github.com/nuomiiiii/lite/web/passkey"
)

func TestMain(m *testing.M) {
	cleanup := instancekey.SetupTempFileForTest()
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:jsonrpc_pkg_test?mode=memory&cache=shared"
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func TestNormalizeGitHubProxySetting(t *testing.T) {
	empty, err := normalizeGitHubProxySetting(" ")
	if err != nil {
		t.Fatalf("empty proxy: %v", err)
	}
	if empty != "" {
		t.Fatalf("empty proxy = %q", empty)
	}
	value, err := normalizeGitHubProxySetting("https://ghfast.top/")
	if err != nil {
		t.Fatalf("valid proxy: %v", err)
	}
	if value != "https://ghfast.top" {
		t.Fatalf("valid proxy = %q", value)
	}
	if _, err := normalizeGitHubProxySetting("ftp://ghfast.top"); err == nil || err.Error() != "代理地址仅支持 HTTP 或 HTTPS" {
		t.Fatalf("scheme error = %v", err)
	}
	if _, err := normalizeGitHubProxySetting("http://127.0.0.1"); err == nil || err.Error() != "代理地址不能是私有或保留地址" {
		t.Fatalf("private error = %v", err)
	}
	if _, err := normalizeGitHubProxySetting(12); err == nil || err.Error() != "GitHub 代理地址必须是字符串" {
		t.Fatalf("type error = %v", err)
	}
}

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

func TestSignInMethodsAllClosed(t *testing.T) {
	passwordOnly := passkey.SignInAvailability{HasPassword: true}
	ssoOnly := passkey.SignInAvailability{PasswordDisabled: true, OAuthEnabled: true, SSOBound: true}
	passkeyOnly := passkey.SignInAvailability{PasswordDisabled: true, PasskeyCount: 1}
	tests := []struct {
		name  string
		avail passkey.SignInAvailability
		cfg   map[string]any
		want  bool
	}{
		{
			name:  "unrelated setting",
			avail: passkey.SignInAvailability{PasswordDisabled: true},
			cfg:   map[string]any{config.SitenameKey: "Lite"},
		},
		{
			name:  "disable password while sso is off and no passkey",
			avail: passwordOnly,
			cfg:   map[string]any{config.DisablePasswordLoginKey: true},
			want:  true,
		},
		{
			name:  "turn off sso while password is disabled and no passkey",
			avail: ssoOnly,
			cfg:   map[string]any{config.OAuthEnabledKey: false},
			want:  true,
		},
		{
			name:  "disable password while a passkey remains",
			avail: passkey.SignInAvailability{HasPassword: true, PasskeyCount: 1},
			cfg:   map[string]any{config.DisablePasswordLoginKey: true},
		},
		{
			name:  "disable password while sso stays bound",
			avail: passkey.SignInAvailability{HasPassword: true, OAuthEnabled: true, SSOBound: true},
			cfg:   map[string]any{config.DisablePasswordLoginKey: true},
		},
		{
			name:  "sso switch alone does not count without a bound account",
			avail: passkey.SignInAvailability{HasPassword: true, OAuthEnabled: true},
			cfg:   map[string]any{config.DisablePasswordLoginKey: true},
			want:  true,
		},
		{
			name:  "turn off sso while password stays on",
			avail: passkey.SignInAvailability{HasPassword: true, OAuthEnabled: true, SSOBound: true},
			cfg:   map[string]any{config.OAuthEnabledKey: false},
		},
		{
			name:  "turn off sso while a passkey remains",
			avail: passkey.SignInAvailability{PasswordDisabled: true, OAuthEnabled: true, SSOBound: true, PasskeyCount: 2},
			cfg:   map[string]any{config.OAuthEnabledKey: false},
		},
		{
			name:  "remove the last passkey is not this settings check",
			avail: passkeyOnly,
			cfg:   map[string]any{config.SitenameKey: "Lite"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := signInMethodsAllClosed(test.avail, test.cfg)
			if got != test.want {
				t.Fatalf("signInMethodsAllClosed() = %v, want %v", got, test.want)
			}
		})
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
		config.ApiKeyKey,
		config.CorsOriginCheckEnabledKey,
		config.CorsAllowedOriginsKey,
		config.WsOriginCheckEnabledKey,
		config.WsAllowedOriginsKey,
		config.AllowRemoteManagementKey,
		config.AllowMCPKey,
		"metric_db_type",
		metricstore.MetricDBDriverKey,
		metricstore.MetricDBDSNKey,
	} {
		if !settingsRequireHumanSession(map[string]any{key: true}) {
			t.Fatalf("%s must require an administrator session", key)
		}
	}
	if settingsRequireHumanSession(map[string]any{config.SitenameKey: "Lite"}) {
		t.Fatal("unrelated settings must still be writable with an API key")
	}
}

func TestValidateMetricDBDSN(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := validateMetricDBDSN("", ""); err != nil {
		t.Fatalf("empty default DSN should pass: %v", err)
	}
	if err := validateMetricDBDSN("sqlite", "./data/metrics.db"); err != nil {
		t.Fatalf("default SQLite DSN should pass: %v", err)
	}
	if err := validateMetricDBDSN("sqlite", "file:./data/sub/metrics.db?cache=shared"); err != nil {
		t.Fatalf("nested ./data/ SQLite DSN should pass: %v", err)
	}
	for _, bad := range []struct {
		driver string
		dsn    string
	}{
		{"mysql", "user:pass@tcp(127.0.0.1:3306)/db?allowAllFiles=true"},
		{"mysql", "user:pass@tcp(127.0.0.1:3306)/db?ALLOWALLFILES=1"},
		{"mysql", "user:pass@tcp(127.0.0.1:3306)/db?allow%2541llFiles=true"},
		{"sqlite", "./data/../outside.db"},
		{"sqlite", "../../etc/passwd"},
		{"sqlite", "./outside.db"},
		{"sqlite", "./data"},
		{"sqlite", "file:../outside.db?mode=rwc"},
		{"sqlite", "file:./data/%2e%2e/evil.db"},
		{"sqlite", "file:./data/%2e%2e/%2e%2e/outside.db"},
		{"sqlite", "file://localhost/etc/passwd"},
		{"sqlite", "./data/lite.db"},
		{"sqlite", "file:./data/lite.db?cache=shared"},
		{"sqlite", "./data/lite.db-wal"},
		{"sqlite", "./data/lite.db-shm"},
		{"sqlite", "./data/lite.db.restore"},
		{"sqlite", "./data/lite-instance.key"},
	} {
		if err := validateMetricDBDSN(bad.driver, bad.dsn); err == nil {
			t.Fatalf("expected (%q, %q) to be rejected", bad.driver, bad.dsn)
		}
	}

	prevDBFile := flags.DatabaseFile
	flags.DatabaseFile = "./data/custom-main.db"
	t.Cleanup(func() { flags.DatabaseFile = prevDBFile })
	if err := validateMetricDBDSN("sqlite", "./data/custom-main.db"); err == nil {
		t.Fatal("expected active flags.DatabaseFile target to be rejected")
	}
	if err := validateMetricDBDSN("sqlite", "./data/custom-main.db-wal"); err == nil {
		t.Fatal("expected active flags.DatabaseFile -wal sidecar to be rejected")
	}

	outsideDir := t.TempDir()
	if err := os.MkdirAll("data", 0o755); err == nil {
		linkPath := filepath.Join("data", "sym-escape")
		if err := os.Symlink(outsideDir, linkPath); err == nil {
			if err := validateMetricDBDSN("sqlite", "./data/sym-escape/metrics.db"); err == nil {
				t.Fatal("expected symlink escape out of ./data/ to be rejected")
			}
		}
	}
}

func TestAdminSensitiveRPCsRejectAPIKey(t *testing.T) {
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{
		Principal:  rpc.NewAPIKeyPrincipal(),
		Permission: rpc.RoleAdmin,
	})
	cases := []struct {
		name string
		call func(context.Context, *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError)
	}{
		{"deleteSession", adminDeleteSession},
		{"deleteAllSessions", adminDeleteAllSessions},
		{"getOidc", adminGetOidc},
		{"getMessageSender", adminGetMessageSender},
		{"startSelfUpdate", adminStartSelfUpdate},
		{"startCloudflared", adminStartCloudflared},
		{"stopCloudflared", adminStopCloudflared},
		{"removeCloudflaredToken", adminRemoveCloudflaredToken},
	}
	for _, tc := range cases {
		_, rpcErr := tc.call(ctx, &rpc.JsonRpcRequest{Version: rpc.RPC_VERSION, ID: 1, Params: map[string]any{}})
		if rpcErr == nil || rpcErr.Code != rpc.PermissionDenied {
			t.Fatalf("%s with API key expected PermissionDenied, got %#v", tc.name, rpcErr)
		}
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
