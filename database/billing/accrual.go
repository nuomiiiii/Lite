package billing

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var BeijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func BeijingDay(value time.Time) time.Time {
	local := value.In(BeijingLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, BeijingLocation)
}

func yesterdayInBeijing(now time.Time) time.Time {
	return BeijingDay(now).AddDate(0, 0, -1)
}

// EnsureAccruedThrough persists every complete Beijing billing day through the
// supplied day. Entry keys make startup catch-up and concurrent queries safe.
func EnsureAccruedThrough(ctx context.Context, db *gorm.DB, through time.Time) error {
	through = BeijingDay(through)
	var versions []models.BillingPriceVersion
	if err := db.WithContext(ctx).
		Where("price_micros > 0 AND billing_cycle_days > 0 AND currency_valid = ?", true).
		Order("effective_from ASC, id ASC").Find(&versions).Error; err != nil {
		return fmt.Errorf("list billing price versions: %w", err)
	}
	for _, version := range versions {
		firstDay := BeijingDay(version.EffectiveFrom)
		lastDay := through
		if version.EffectiveTo != nil {
			versionLastDay := BeijingDay(version.EffectiveTo.Add(-time.Nanosecond))
			if versionLastDay.Before(lastDay) {
				lastDay = versionLastDay
			}
		}
		if version.ExpiredAt != nil {
			expiryLastDay := BeijingDay(version.ExpiredAt.Add(-time.Nanosecond))
			if expiryLastDay.Before(lastDay) {
				lastDay = expiryLastDay
			}
		}
		for day := firstDay; !day.After(lastDay); day = day.AddDate(0, 0, 1) {
			entry, ok, err := accrualEntryForInterval(db, version, day, day.AddDate(0, 0, 1))
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if err := db.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "entry_key"}},
				DoNothing: true,
			}).Create(&entry).Error; err != nil {
				return fmt.Errorf("persist billing accrual %s: %w", entry.EntryKey, err)
			}
		}
	}
	return nil
}

func CurrentDayAccruals(ctx context.Context, db *gorm.DB, now time.Time) ([]models.BillingEntry, error) {
	dayStart := BeijingDay(now)
	dayEnd := dayStart.AddDate(0, 0, 1)
	var versions []models.BillingPriceVersion
	if err := db.WithContext(ctx).
		Where("price_micros > 0 AND billing_cycle_days > 0 AND currency_valid = ?", true).
		Where("effective_from < ?", dayEnd.UTC()).
		Where("effective_to IS NULL OR effective_to > ?", dayStart.UTC()).
		Order("effective_from ASC, id ASC").Find(&versions).Error; err != nil {
		return nil, fmt.Errorf("list current billing versions: %w", err)
	}
	entries := make([]models.BillingEntry, 0, len(versions))
	for _, version := range versions {
		entry, ok, err := accrualEntryForInterval(db, version, dayStart, dayEnd)
		if err != nil {
			return nil, err
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func accrualEntryForInterval(db *gorm.DB, version models.BillingPriceVersion, intervalStart, intervalEnd time.Time) (models.BillingEntry, bool, error) {
	start := intervalStart.UTC()
	end := intervalEnd.UTC()
	if version.EffectiveFrom.After(start) {
		start = version.EffectiveFrom.UTC()
	}
	if version.EffectiveTo != nil && version.EffectiveTo.Before(end) {
		end = version.EffectiveTo.UTC()
	}
	if version.ExpiredAt != nil && version.ExpiredAt.Before(end) {
		end = version.ExpiredAt.UTC()
	}
	if !end.After(start) {
		return models.BillingEntry{}, false, nil
	}
	seconds := end.Sub(start).Nanoseconds()
	cycleNanos := int64(version.BillingCycleDays) * int64(24*time.Hour)
	amount, err := multiplyRatio(version.PriceMicros, seconds, cycleNanos)
	if err != nil {
		return models.BillingEntry{}, false, fmt.Errorf("calculate accrual for price version %d: %w", version.ID, err)
	}
	if amount == 0 {
		return models.BillingEntry{}, false, nil
	}
	snapshotID, usdAmount, err := lockedConversionFromVersion(version, amount)
	if err != nil {
		return models.BillingEntry{}, false, err
	}
	if snapshotID == nil {
		snapshotID, usdAmount, err = SnapshotConversion(db, amount, version.Currency)
		if err != nil {
			return models.BillingEntry{}, false, err
		}
	}
	day := BeijingDay(intervalStart)
	versionID := version.ID
	return models.BillingEntry{
		EntryKey:             fmt.Sprintf("base:%s:%s:%d", version.Client, day.Format(time.DateOnly), version.ID),
		Client:               version.Client,
		ClientName:           version.ClientName,
		Type:                 EntryTypeBaseAccrual,
		Day:                  day.Format(time.DateOnly),
		OccurredAt:           end,
		OriginalAmountMicros: amount,
		OriginalCurrency:     version.Currency,
		FXSnapshotID:         snapshotID,
		USDAmountMicros:      usdAmount,
		PriceVersionID:       &versionID,
		SourceRef:            fmt.Sprintf("price-version:%d", version.ID),
		Note:                 "daily cost accrual",
	}, true, nil
}

func lockedConversionFromVersion(version models.BillingPriceVersion, nativeAmount int64) (*uint64, *int64, error) {
	if version.USDPriceMicros == nil || version.FXSnapshotID == nil || version.PriceMicros == 0 {
		return nil, nil, nil
	}
	usd, err := multiplyRatio(*version.USDPriceMicros, nativeAmount, version.PriceMicros)
	if err != nil {
		return nil, nil, err
	}
	return version.FXSnapshotID, &usd, nil
}

func multiplyRatio(amount, numerator, denominator int64) (int64, error) {
	if denominator <= 0 || numerator < 0 {
		return 0, fmt.Errorf("invalid ratio")
	}
	value := new(big.Rat).SetInt64(amount)
	value.Mul(value, new(big.Rat).SetFrac64(numerator, denominator))
	return roundRatToInt64(value)
}
