package auditlog

import (
	"strings"
	"testing"
)

func TestAllowMCPUsesTheOnScreenName(t *testing.T) {
	changes := SettingChanges(
		map[string]any{"allow_mcp": false},
		map[string]any{"allow_mcp": true},
	)
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Key != "audit.bool_on" || changes[0].Params["setting"] != "allow_mcp" {
		t.Fatalf("change = %+v", changes[0])
	}
	stored := Message(changes[0].Key, changes[0].Params)
	for _, label := range []string{
		"启用 MCP 代理",
		"Enable MCP Proxy",
		"MCP プロキシを有効にする",
		"啟用 MCP 服務",
	} {
		if !strings.Contains(stored, label) {
			t.Fatalf("stored log missing %q: %s", label, stored)
		}
	}
	if strings.Contains(stored, "allow_mcp") && strings.Contains(stored, "开启了「allow_mcp」") {
		t.Fatal("search text used the raw key instead of the UI label")
	}
}

func TestSecretValuesStayOutOfTheLog(t *testing.T) {
	secret := "sk-live-should-not-appear"
	changes := SettingChanges(
		map[string]any{"api_key": "old-secret-value"},
		map[string]any{"api_key": secret},
	)
	if len(changes) != 1 || changes[0].Key != "audit.secret_replace" {
		t.Fatalf("changes = %+v", changes)
	}
	stored := Message(changes[0].Key, changes[0].Params)
	if strings.Contains(stored, secret) || strings.Contains(stored, "old-secret-value") {
		t.Fatal(stored)
	}
	if !strings.Contains(stored, "站点 API 密钥") || !strings.Contains(stored, "更换了") {
		t.Fatal(stored)
	}
}

func TestUnchangedAndLongText(t *testing.T) {
	if changes := SettingChanges(
		map[string]any{"allow_remote_management": true},
		map[string]any{"allow_remote_management": true},
	); len(changes) != 0 {
		t.Fatalf("unchanged setting was logged: %+v", changes)
	}
	body := "<script>secret-body</script>"
	changes := SettingChanges(
		map[string]any{"custom_head": ""},
		map[string]any{"custom_head": body},
	)
	if len(changes) != 1 || changes[0].Key != "audit.text_update" {
		t.Fatalf("changes = %+v", changes)
	}
	stored := Message(changes[0].Key, changes[0].Params)
	if strings.Contains(stored, "secret-body") {
		t.Fatal(stored)
	}
	if !strings.Contains(stored, "自定义 Head") {
		t.Fatal(stored)
	}
}

func TestValueChangeKeepsFromAndTo(t *testing.T) {
	changes := SettingChanges(
		map[string]any{"sitename": "旧名称", "metric_db_driver": "sqlite"},
		map[string]any{"sitename": "新名称", "metric_db_driver": "mysql"},
	)
	if len(changes) != 1 {
		t.Fatalf("driver should not get its own line: %+v", changes)
	}
	stored := Message(changes[0].Key, changes[0].Params)
	if !strings.Contains(stored, "站点名称") || !strings.Contains(stored, "旧名称") || !strings.Contains(stored, "新名称") {
		t.Fatal(stored)
	}
}
