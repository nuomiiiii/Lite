package timeutil

import (
	"testing"
	"time"
)

func TestSystemDateHelpersUseSystemLocal(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	t.Cleanup(func() { time.Local = originalLocal })

	lateUTC := time.Date(2026, 7, 17, 16, 30, 0, 0, time.UTC)
	earlyUTC := time.Date(2026, 7, 18, 1, 30, 0, 0, time.UTC)
	if !SameSystemDate(lateUTC, earlyUTC) {
		t.Fatal("instants on the same system-local date should match")
	}
	if got := FormatSystemDate(lateUTC); got != "2026-07-18" {
		t.Fatalf("formatted system date = %q, want 2026-07-18", got)
	}
	if got := SystemDateDistance(lateUTC.AddDate(0, 0, -2), lateUTC); got != 2 {
		t.Fatalf("system date distance = %d, want 2", got)
	}
}

func TestFormatSystemDateReturnsEmptyForZeroTime(t *testing.T) {
	if got := FormatSystemDate(time.Time{}); got != "" {
		t.Fatalf("formatted zero time = %q, want empty", got)
	}
}

func TestBeijingDayReachedUsesChinaCalendarNotUTC(t *testing.T) {
	expire := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC) // 2026-09-02 00:00 Beijing
	before := time.Date(2026, 9, 1, 15, 59, 0, 0, time.UTC)
	atMidnight := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	after := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC) // 01:00 Beijing on 9.2
	if BeijingDayReached(before, expire) {
		t.Fatal("9.1 23:59 Beijing should not renew a 9.2 expiry")
	}
	if !BeijingDayReached(atMidnight, expire) {
		t.Fatal("Beijing midnight on the expiry date should renew")
	}
	if !BeijingDayReached(after, expire) {
		t.Fatal("01:00 Beijing on the expiry date should renew")
	}
	if FormatBeijingDate(expire) != "2026-09-02" {
		t.Fatalf("beijing date = %q, want 2026-09-02", FormatBeijingDate(expire))
	}
}
