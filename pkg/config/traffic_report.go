package config

import (
	"fmt"
	"strings"
	"time"
)

const DefaultTrafficReportTime = "00:00"

// NormalizeTrafficReportTime validates and normalizes the Beijing report time.
func NormalizeTrafficReportTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return "", fmt.Errorf("traffic report time must use HH:mm format")
	}
	return value, nil
}
