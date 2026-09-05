package themehttp

import (
	"net/netip"
	"testing"
)

func TestBlockedAddrRanges(t *testing.T) {
	blocked := []string{
		"0.0.0.0",
		"0.1.2.3",
		"127.0.0.1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.1.1",
		"224.0.0.1",
		"255.255.255.255",
		"100.64.0.1",
		"192.0.0.1",
		"192.0.2.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"240.0.0.1",
		"::1",
		"::",
		"fe80::1",
		"fc00::1",
		"ff02::1",
		"2001:db8::1",
		"fec0::1",
		"::ffff:127.0.0.1",
		"::ffff:10.0.0.1",
		"::ffff:192.168.0.1",
		"::ffff:169.254.1.1",
		"::ffff:100.64.0.1",
	}
	for _, raw := range blocked {
		addr := netip.MustParseAddr(raw)
		if !blockedAddr(addr) {
			t.Errorf("blockedAddr(%s) = false, want true", raw)
		}
	}

	allowed := []string{
		"1.1.1.1",
		"8.8.8.8",
		"8.8.4.4",
		"2001:4860:4860::8888",
		"::ffff:1.1.1.1",
		"::ffff:8.8.8.8",
	}
	for _, raw := range allowed {
		addr := netip.MustParseAddr(raw)
		if blockedAddr(addr) {
			t.Errorf("blockedAddr(%s) = true, want false", raw)
		}
	}
}

func TestSizeLimits(t *testing.T) {
	if MaxArchive != 128<<20 {
		t.Fatalf("MaxArchive = %d, want 128 MiB", MaxArchive)
	}
	if MaxCatalog != 2<<20 {
		t.Fatalf("MaxCatalog = %d, want 2 MiB", MaxCatalog)
	}
	if MaxPreview != 8<<20 {
		t.Fatalf("MaxPreview = %d, want 8 MiB", MaxPreview)
	}
	if MaxGitHubJSON != 2<<20 {
		t.Fatalf("MaxGitHubJSON = %d, want 2 MiB", MaxGitHubJSON)
	}
}
