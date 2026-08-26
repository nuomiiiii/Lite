package billing

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

const (
	PriceSourceMigration  = "migration"
	PriceSourceClientEdit = "client_edit"
	PriceSourceRenewal    = "renewal"
)

func EnsureInitialPriceVersions(db *gorm.DB, effectiveAt time.Time) error {
	effectiveAt = effectiveAt.UTC()
	return db.Transaction(func(tx *gorm.DB) error {
		var clients []models.Client
		if err := tx.Find(&clients).Error; err != nil {
			return fmt.Errorf("list clients for billing migration: %w", err)
		}
		for _, client := range clients {
			var count int64
			if err := tx.Model(&models.BillingPriceVersion{}).
				Where("client = ?", client.UUID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			version, err := priceVersionFromClient(tx, client, PriceSourceMigration, effectiveAt)
			if err != nil {
				return fmt.Errorf("create initial billing version for %s: %w", client.UUID, err)
			}
			if err := tx.Create(&version).Error; err != nil {
				return fmt.Errorf("save initial billing version for %s: %w", client.UUID, err)
			}
		}
		return nil
	})
}

func CapturePriceVersion(tx *gorm.DB, existing models.Client, updates map[string]interface{}, source string, effectiveAt time.Time) error {
	next, changed, err := clientAfterBillingUpdate(existing, updates)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	effectiveAt = effectiveAt.UTC()
	if err := tx.Model(&models.BillingPriceVersion{}).
		Where("client = ? AND effective_to IS NULL", existing.UUID).
		Update("effective_to", effectiveAt).Error; err != nil {
		return fmt.Errorf("close previous billing version: %w", err)
	}
	version, err := priceVersionFromClient(tx, next, source, effectiveAt)
	if err != nil {
		return err
	}
	if err := tx.Create(&version).Error; err != nil {
		return fmt.Errorf("create billing version: %w", err)
	}
	if source != PriceSourceMigration && version.PriceMicros > 0 && version.BillingCycleDays == -1 {
		priceChanged := next.Price != existing.Price || next.BillingCycle != existing.BillingCycle || next.Currency != existing.Currency
		if priceChanged {
			if _, err := createOneTimeEntry(tx, version, effectiveAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func priceVersionFromClient(db *gorm.DB, client models.Client, source string, effectiveAt time.Time) (models.BillingPriceVersion, error) {
	priceMicros, err := MicrosFromFloat(client.Price)
	if err != nil {
		return models.BillingPriceVersion{}, err
	}
	currency, valid := NormalizeCurrency(client.Currency)
	version := models.BillingPriceVersion{
		Client:           client.UUID,
		ClientName:       client.Name,
		Region:           client.Region,
		Group:            client.Group,
		PriceMicros:      priceMicros,
		Currency:         currency,
		CurrencyValid:    valid,
		BillingCycleDays: client.BillingCycle,
		ExpiredAt:        normalizeOptionalTime(client.ExpiredAt),
		EffectiveFrom:    effectiveAt.UTC(),
		Source:           source,
	}
	if err := attachPriceVersionFX(db, &version); err != nil {
		return models.BillingPriceVersion{}, err
	}
	return version, nil
}

func attachPriceVersionFX(db *gorm.DB, version *models.BillingPriceVersion) error {
	if db == nil || version.PriceMicros <= 0 || !version.CurrencyValid {
		return nil
	}
	snapshotID, usd, err := SnapshotConversion(db, version.PriceMicros, version.Currency)
	if err != nil {
		return err
	}
	version.FXSnapshotID = snapshotID
	version.USDPriceMicros = usd
	return nil
}

func BackfillPriceVersionFX(db *gorm.DB) error {
	var versions []models.BillingPriceVersion
	if err := db.Where("fx_snapshot_id IS NULL AND price_micros > 0 AND currency_valid = ?", true).
		Find(&versions).Error; err != nil {
		return fmt.Errorf("list billing versions missing FX snapshots: %w", err)
	}
	for _, version := range versions {
		if err := attachPriceVersionFX(db, &version); err != nil {
			return fmt.Errorf("backfill billing FX for version %d: %w", version.ID, err)
		}
		if version.FXSnapshotID == nil {
			return nil
		}
		if err := db.Model(&models.BillingPriceVersion{}).Where("id = ?", version.ID).Updates(map[string]interface{}{
			"fx_snapshot_id":   version.FXSnapshotID,
			"usd_price_micros": version.USDPriceMicros,
		}).Error; err != nil {
			return fmt.Errorf("save billing FX for version %d: %w", version.ID, err)
		}
	}
	return nil
}

func clientAfterBillingUpdate(existing models.Client, updates map[string]interface{}) (models.Client, bool, error) {
	next := existing
	if value, ok := updates["price"]; ok {
		price, err := numericFloat64(value)
		if err != nil {
			return models.Client{}, false, fmt.Errorf("price: %w", err)
		}
		next.Price = price
	}
	if value, ok := updates["billing_cycle"]; ok {
		cycle, err := numericInt(value)
		if err != nil {
			return models.Client{}, false, fmt.Errorf("billing_cycle: %w", err)
		}
		next.BillingCycle = cycle
	}
	if value, ok := updates["currency"]; ok {
		currency, ok := value.(string)
		if !ok {
			return models.Client{}, false, fmt.Errorf("currency must be a string")
		}
		next.Currency = strings.TrimSpace(currency)
	}
	if value, ok := updates["expired_at"]; ok {
		switch typed := value.(type) {
		case nil:
			next.ExpiredAt = nil
		case time.Time:
			t := typed.UTC()
			next.ExpiredAt = &t
		case *time.Time:
			next.ExpiredAt = normalizeOptionalTime(typed)
		default:
			return models.Client{}, false, fmt.Errorf("expired_at has an unsupported value")
		}
	}
	if value, ok := updates["name"].(string); ok {
		next.Name = value
	}
	if value, ok := updates["region"].(string); ok {
		next.Region = value
	}
	if value, ok := updates["group"].(string); ok {
		next.Group = value
	}
	changed := next.Price != existing.Price ||
		next.BillingCycle != existing.BillingCycle ||
		next.Currency != existing.Currency ||
		!optionalTimesEqual(next.ExpiredAt, existing.ExpiredAt)
	return next, changed, nil
}

func numericFloat64(value interface{}) (float64, error) {
	var result float64
	switch typed := value.(type) {
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int:
		result = float64(typed)
	case int64:
		result = float64(typed)
	case uint:
		result = float64(typed)
	case uint64:
		result = float64(typed)
	default:
		return 0, fmt.Errorf("must be numeric")
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, fmt.Errorf("must be finite")
	}
	if _, err := MicrosFromFloat(result); err != nil {
		return 0, err
	}
	return result, nil
}

func numericInt(value interface{}) (int, error) {
	floatValue, err := numericFloat64(value)
	if err != nil || math.Trunc(floatValue) != floatValue || floatValue < math.MinInt || floatValue > math.MaxInt {
		return 0, fmt.Errorf("must be an integer")
	}
	return int(floatValue), nil
}

func normalizeOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func optionalTimesEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
