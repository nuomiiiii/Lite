package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

const (
	EntryTypeBaseAccrual  = "base_accrual"
	EntryTypeTrafficReset = "traffic_reset"
	EntryTypeIPChange     = "ip_change"
	EntryTypeOneTime      = "one_time"
	EntryTypeAdjustment   = "adjustment"
	EntryTypeReversal     = "reversal"
)

type TrafficResetInput struct {
	Client         string
	Amount         string
	Currency       string
	OccurredAt     time.Time
	Note           string
	IdempotencyKey string
	Operator       string
}

func CreateTrafficResetEntry(ctx context.Context, db *gorm.DB, input TrafficResetInput) (models.BillingEntry, error) {
	return createAddonEntry(ctx, db, input, EntryTypeTrafficReset, "traffic-reset:")
}

func CreateIPChangeEntry(ctx context.Context, db *gorm.DB, input TrafficResetInput) (models.BillingEntry, error) {
	return createAddonEntry(ctx, db, input, EntryTypeIPChange, "ip-change:")
}

func CreateOneTimeFeeEntry(ctx context.Context, db *gorm.DB, input TrafficResetInput) (models.BillingEntry, error) {
	return createAddonEntry(ctx, db, input, EntryTypeAdjustment, "one-time-fee:")
}

func createAddonEntry(ctx context.Context, db *gorm.DB, input TrafficResetInput, entryType, keyPrefix string) (models.BillingEntry, error) {
	var result models.BillingEntry
	if strings.TrimSpace(input.Client) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		return result, invalidInputf("client and idempotency_key are required")
	}
	amount, err := ParseAmountMicros(input.Amount)
	if err != nil || amount <= 0 {
		return result, invalidInputf("amount must be greater than zero")
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entryKey := keyPrefix + input.Client + ":" + input.IdempotencyKey
		if err := tx.Where("entry_key = ?", entryKey).First(&result).Error; err == nil {
			return nil
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		var client models.Client
		if err := tx.Where("uuid = ?", input.Client).First(&client).Error; err != nil {
			return fmt.Errorf("find client: %w", err)
		}
		currencySource := input.Currency
		if strings.TrimSpace(currencySource) == "" {
			currencySource = client.Currency
		}
		currency, valid := NormalizeCurrency(currencySource)
		if !valid {
			return invalidInputf("currency is not a recognized ISO 4217 code")
		}
		occurredAt := input.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = time.Now().In(BeijingLocation).UTC()
		} else {
			occurredAt = occurredAt.UTC()
		}
		snapshotID, usdAmount, err := SnapshotConversion(tx, amount, currency)
		if err != nil {
			return err
		}
		result = models.BillingEntry{
			EntryKey:             entryKey,
			Client:               client.UUID,
			ClientName:           client.Name,
			Type:                 entryType,
			Day:                  BeijingDay(occurredAt).Format(time.DateOnly),
			OccurredAt:           occurredAt,
			OriginalAmountMicros: amount,
			OriginalCurrency:     currency,
			FXSnapshotID:         snapshotID,
			USDAmountMicros:      usdAmount,
			SourceRef:            input.IdempotencyKey,
			Note:                 strings.TrimSpace(input.Note),
			Operator:             input.Operator,
		}
		return tx.Create(&result).Error
	})
	return result, err
}

func VoidEntry(ctx context.Context, db *gorm.DB, id uint64, reason, operator string) (models.BillingEntry, error) {
	var result models.BillingEntry
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return result, invalidInputf("reason is required")
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var original models.BillingEntry
		if err := tx.First(&original, id).Error; err != nil {
			return err
		}
		if original.Type == EntryTypeReversal {
			return invalidInputf("a reversal entry cannot be voided")
		}
		entryKey := fmt.Sprintf("reversal:%d", original.ID)
		if err := tx.Where("entry_key = ?", entryKey).First(&result).Error; err == nil {
			return nil
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		usdAmount := negateOptionalAmount(original.USDAmountMicros)
		snapshotID := original.FXSnapshotID
		originalID := original.ID
		result = models.BillingEntry{
			EntryKey:             entryKey,
			Client:               original.Client,
			ClientName:           original.ClientName,
			Type:                 EntryTypeReversal,
			Day:                  original.Day,
			OccurredAt:           time.Now().UTC(),
			OriginalAmountMicros: -original.OriginalAmountMicros,
			OriginalCurrency:     original.OriginalCurrency,
			FXSnapshotID:         snapshotID,
			USDAmountMicros:      usdAmount,
			ReversalOf:           &originalID,
			SourceRef:            fmt.Sprintf("void:%d", original.ID),
			Note:                 reason,
			Operator:             operator,
		}
		return tx.Create(&result).Error
	})
	return result, err
}

func createOneTimeEntry(tx *gorm.DB, version models.BillingPriceVersion, occurredAt time.Time) (models.BillingEntry, error) {
	entryKey := fmt.Sprintf("one-time:%s:%d", version.Client, version.ID)
	var existing models.BillingEntry
	if err := tx.Where("entry_key = ?", entryKey).First(&existing).Error; err == nil {
		return existing, nil
	} else if err != gorm.ErrRecordNotFound {
		return models.BillingEntry{}, err
	}
	snapshotID, usdAmount, err := SnapshotConversion(tx, version.PriceMicros, version.Currency)
	if err != nil {
		return models.BillingEntry{}, err
	}
	versionID := version.ID
	entry := models.BillingEntry{
		EntryKey:             entryKey,
		Client:               version.Client,
		ClientName:           version.ClientName,
		Type:                 EntryTypeOneTime,
		Day:                  BeijingDay(occurredAt).Format(time.DateOnly),
		OccurredAt:           occurredAt.UTC(),
		OriginalAmountMicros: version.PriceMicros,
		OriginalCurrency:     version.Currency,
		FXSnapshotID:         snapshotID,
		USDAmountMicros:      usdAmount,
		PriceVersionID:       &versionID,
		SourceRef:            fmt.Sprintf("price-version:%d", version.ID),
		Note:                 "one-time billing configuration",
	}
	return entry, tx.Create(&entry).Error
}

func negateOptionalAmount(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := -*value
	return &result
}
