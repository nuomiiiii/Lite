package clients

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

var trafficCycleLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func normalizeTrafficType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "sum", "max", "min", "up", "down":
		return value, nil
	default:
		return "", fmt.Errorf("traffic type must be one of sum, max, min, up, or down")
	}
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, trafficCycleLocation).Day()
}

func cycleBoundary(year int, month time.Month, resetDay int) time.Time {
	if last := daysInMonth(year, month); resetDay > last {
		resetDay = last
	}
	return time.Date(year, month, resetDay, 0, 0, 0, 0, trafficCycleLocation)
}

func currentTrafficCycle(resetDay *int, now time.Time) string {
	if resetDay == nil || *resetDay < 1 || *resetDay > 31 {
		return ""
	}
	local := now.In(trafficCycleLocation)
	boundary := cycleBoundary(local.Year(), local.Month(), *resetDay)
	if local.Before(boundary) {
		previous := local.AddDate(0, -1, 0)
		boundary = cycleBoundary(previous.Year(), previous.Month(), *resetDay)
	}
	return boundary.Format(time.DateOnly)
}

func applyClientDisplayFields(client *models.Client, now time.Time) bool {
	changed := false
	cycle := currentTrafficCycle(client.TrafficResetDay, now)
	if client.TrafficResetAllowance < 0 || cycle == "" || client.TrafficResetCycle != cycle {
		if client.TrafficResetAllowance != 0 || client.TrafficResetCycle != "" {
			client.TrafficResetAllowance = 0
			client.TrafficResetCycle = ""
			changed = true
		}
	}
	typeName := strings.ToLower(strings.TrimSpace(client.TrafficLimitType))
	if _, err := normalizeTrafficType(typeName); err != nil {
		typeName = "sum"
	}
	client.EffectiveTrafficLimit = client.TrafficLimit
	client.EffectiveTrafficType = typeName
	if client.TrafficResetAllowance > 0 {
		if client.TrafficLimit > math.MaxInt64-client.TrafficResetAllowance {
			client.EffectiveTrafficLimit = math.MaxInt64
		} else {
			client.EffectiveTrafficLimit += client.TrafficResetAllowance
		}
	}
	if client.RegionOverride != "" {
		client.Region = client.RegionOverride
	}
	return changed
}

func applyClientDisplayFieldsAndPersist(db *gorm.DB, clients []models.Client, now time.Time) error {
	expired := make([]string, 0)
	for index := range clients {
		if applyClientDisplayFields(&clients[index], now) {
			expired = append(expired, clients[index].UUID)
		}
	}
	if len(expired) == 0 {
		return nil
	}
	return db.Model(&models.Client{}).
		Where("uuid IN ?", expired).
		Updates(map[string]any{
			"traffic_reset_allowance": 0,
			"traffic_reset_cycle":     "",
		}).Error
}

// EffectiveTrafficLimit returns the active quota and counting method for the
// current billing cycle without changing historical traffic measurements.
func EffectiveTrafficLimit(client models.Client, now time.Time) (int64, string) {
	applyClientDisplayFields(&client, now)
	return client.EffectiveTrafficLimit, client.EffectiveTrafficType
}
