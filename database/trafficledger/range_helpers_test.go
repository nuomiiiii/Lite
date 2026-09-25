package trafficledger

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

func ensureRangeWithCalculator(ctx context.Context, db *gorm.DB, clientIDs []string, startDay, endDay time.Time, calculate usageCalculator) error {
	if calculate == nil {
		return fmt.Errorf("traffic ledger calculator is nil")
	}
	return ensureRangeWithDailyCalculator(ctx, db, clientIDs, startDay, endDay,
		func(ctx context.Context, clientID string, start, end time.Time) (map[string]Usage, error) {
			result := make(map[string]Usage)
			for day := BeijingDay(start); day.Before(BeijingDay(end)); day = day.AddDate(0, 0, 1) {
				usage, err := calculate(ctx, clientID, day.UTC(), day.AddDate(0, 0, 1).UTC().Add(-time.Nanosecond))
				if err != nil {
					return nil, err
				}
				result[dayKey(day)] = usage
			}
			return result, nil
		})
}
