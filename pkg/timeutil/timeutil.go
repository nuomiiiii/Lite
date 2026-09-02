package timeutil

import (
	"time"
)

// BeijingLocation is the calendar used for expiry and auto-renewal.
// Docker TZ must not change whether 9.2 means 2 September in China.
var BeijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// SameSystemDate compares calendar dates in the operating system timezone.
func SameSystemDate(left, right time.Time) bool {
	return sameDateIn(left, right, time.Local)
}

func sameDateIn(left, right time.Time, location *time.Location) bool {
	left = left.In(location)
	right = right.In(location)
	ly, lm, ld := left.Date()
	ry, rm, rd := right.Date()
	return ly == ry && lm == rm && ld == rd
}

// SameBeijingDate compares calendar dates in Asia/Shanghai.
func SameBeijingDate(left, right time.Time) bool {
	return sameDateIn(left, right, BeijingLocation)
}

// BeijingDay returns midnight of the Asia/Shanghai calendar day containing value.
func BeijingDay(value time.Time) time.Time {
	local := value.In(BeijingLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, BeijingLocation)
}

// BeijingDayReached reports whether now is on or after expire's Beijing calendar day.
func BeijingDayReached(now, expire time.Time) bool {
	return !BeijingDay(now).Before(BeijingDay(expire))
}

// FormatSystemDate formats an instant as a system-local calendar date.
func FormatSystemDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(time.Local).Format("2006-01-02")
}

// FormatBeijingDate formats an instant as an Asia/Shanghai calendar date.
func FormatBeijingDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(BeijingLocation).Format("2006-01-02")
}

// SystemDateDistance returns the number of local calendar boundaries between
// two instants, independent of daylight-saving day length.
func SystemDateDistance(from, to time.Time) int {
	fy, fm, fd := from.In(time.Local).Date()
	ty, tm, td := to.In(time.Local).Date()
	fromDate := time.Date(fy, fm, fd, 0, 0, 0, 0, time.UTC)
	toDate := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)
	return int(toDate.Sub(fromDate) / (24 * time.Hour))
}
