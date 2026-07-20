package config

import "testing"

func TestNormalizeTrafficReportTime(t *testing.T) {
	for _, valid := range []string{"00:00", "09:30", "23:59"} {
		got, err := NormalizeTrafficReportTime(valid)
		if err != nil || got != valid {
			t.Fatalf("NormalizeTrafficReportTime(%q) = %q, %v", valid, got, err)
		}
	}
	for _, invalid := range []string{"", "9:30", "24:00", "12:60", "12:30:00"} {
		if _, err := NormalizeTrafficReportTime(invalid); err == nil {
			t.Fatalf("NormalizeTrafficReportTime(%q) unexpectedly succeeded", invalid)
		}
	}
}
