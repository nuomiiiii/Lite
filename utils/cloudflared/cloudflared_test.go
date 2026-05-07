package cloudflared_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/komari-monitor/komari/utils/cloudflared"
)

func TestStatusDoesNotExposeToken(t *testing.T) {
	const token = "test-cloudflare-token"
	t.Setenv("KOMARI_CLOUDFLARED_TOKEN", token)

	status := cloudflared.Status()
	if !status.EnvTokenPresent {
		t.Fatalf("expected environment token to be detected")
	}

	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("failed to marshal status: %v", err)
	}

	if strings.Contains(string(payload), token) {
		t.Fatalf("expected status payload not to contain the raw token")
	}
}
