package timeutil

import (
	"time"
)

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

func FormatSystemDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.In(time.Local).Format("2006-01-02")
}
