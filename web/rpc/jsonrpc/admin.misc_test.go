package jsonrpc

import (
	"testing"

	"github.com/nuomiiiii/lite/database/metricstore"
	"github.com/nuomiiiii/lite/pkg/config"
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
