package clients

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/trafficreset"
	"gorm.io/gorm"
)

func normalizeTrafficType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "sum", "max", "min", "up", "down":
		return value, nil
	default:
		return "", fmt.Errorf("traffic type must be one of sum, max, min, up, or down")
	}
}

func currentTrafficCycleAt(resetDay *int, clock, timezone string, now time.Time) string {
	return trafficreset.FromFields(resetDay, clock, timezone).CycleKey(now)
}

func applyClientDisplayFields(client *models.Client, now time.Time) bool {
	changed := false
	if next, ok := trafficreset.RemapLegacyCustomCycleKey(
		client.TrafficResetCycle, client.TrafficResetDay, client.TrafficResetTime, client.TrafficResetTimezone, now,
	); ok {
		client.TrafficResetCycle = next
		changed = true
	}
	cycle := currentTrafficCycleAt(client.TrafficResetDay, client.TrafficResetTime, client.TrafficResetTimezone, now)
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

// PersistTrafficDisplayFields writes cycle rollovers that the list read only
// applies in memory. The minute traffic check calls it, so opening the server
// list does not update every row.
//
// PersistTrafficDisplayFields 把列表读取时只在内存里算的周期切换写回数据库。
// 由每分钟的流量检查调用，因此打开服务器列表时不再逐台更新。
func PersistTrafficDisplayFields() error {
	db := dbcore.GetDBInstance()
	var list []models.Client
	if err := db.Find(&list).Error; err != nil {
		return err
	}
	return applyClientDisplayFieldsAndPersist(db, list, time.Now().UTC())
}

func applyClientDisplayFieldsAndPersist(db *gorm.DB, clients []models.Client, now time.Time) error {
	updated := false
	for index := range clients {
		if !applyClientDisplayFields(&clients[index], now) {
			continue
		}
		if err := db.Model(&models.Client{}).
			Where("uuid = ?", clients[index].UUID).
			Updates(map[string]any{
				"traffic_reset_allowance": clients[index].TrafficResetAllowance,
				"traffic_reset_cycle":     clients[index].TrafficResetCycle,
			}).Error; err != nil {
			if updated {
				invalidateClientListCache()
			}
			return err
		}
		updated = true
	}
	if updated {
		invalidateClientListCache()
	}
	return nil
}

// EffectiveTrafficLimit returns the active quota and counting method for the
// current billing cycle without changing historical traffic measurements.
func EffectiveTrafficLimit(client models.Client, now time.Time) (int64, string) {
	applyClientDisplayFields(&client, now)
	return client.EffectiveTrafficLimit, client.EffectiveTrafficType
}
