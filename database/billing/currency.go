package billing

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/nuomiiiii/lite/database/models"
	xcurrency "golang.org/x/text/currency"
	"gorm.io/gorm"
)

const MicrosPerUnit int64 = 1_000_000

var legacyCurrencies = map[string]string{
	"$": "USD", "US$": "USD", "USD": "USD",
	"¥": "CNY", "￥": "CNY", "RMB": "CNY", "CNY": "CNY",
	"€": "EUR", "EUR": "EUR",
	"£": "GBP", "GBP": "GBP",
	"C$": "CAD", "CA$": "CAD", "CAD": "CAD",
}

func NormalizeCurrency(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	upper := strings.ToUpper(trimmed)
	if normalized, ok := legacyCurrencies[upper]; ok {
		return normalized, true
	}
	if _, err := xcurrency.ParseISO(upper); err == nil {
		return upper, true
	}
	return trimmed, false
}

func CanonicalNativeCurrencies(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		canonical, ok := NormalizeCurrency(value)
		if !ok {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out
}

func ReconcileStoredCurrencies(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	var versions []models.BillingPriceVersion
	if err := db.Find(&versions).Error; err != nil {
		return fmt.Errorf("list billing versions for currency reconcile: %w", err)
	}
	for _, version := range versions {
		canonical, valid := NormalizeCurrency(version.Currency)
		if !valid || (version.Currency == canonical && version.CurrencyValid) {
			continue
		}
		if err := db.Model(&models.BillingPriceVersion{}).Where("id = ?", version.ID).
			Select("currency", "currency_valid").
			Updates(map[string]interface{}{
				"currency":       canonical,
				"currency_valid": valid,
			}).Error; err != nil {
			return fmt.Errorf("reconcile billing version %d currency: %w", version.ID, err)
		}
	}
	var entries []models.BillingEntry
	if err := db.Find(&entries).Error; err != nil {
		return fmt.Errorf("list billing entries for currency reconcile: %w", err)
	}
	for _, entry := range entries {
		canonical, valid := NormalizeCurrency(entry.OriginalCurrency)
		if !valid || entry.OriginalCurrency == canonical {
			continue
		}
		if err := db.Model(&models.BillingEntry{}).Where("id = ?", entry.ID).
			Update("original_currency", canonical).Error; err != nil {
			return fmt.Errorf("reconcile billing entry %d currency: %w", entry.ID, err)
		}
	}
	return nil
}

func ParseAmountMicros(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("amount is empty")
	}
	negative := false
	if value[0] == '+' || value[0] == '-' {
		if value[0] == '-' {
			negative = true
		}
		value = value[1:]
	}
	if value == "" || strings.Count(value, ".") > 1 {
		return 0, fmt.Errorf("invalid amount")
	}
	parts := strings.SplitN(value, ".", 2)
	whole := parts[0]
	if whole == "" {
		whole = "0"
	}
	for _, r := range whole {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid amount")
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 6 {
		return 0, fmt.Errorf("amount supports at most 6 decimal places")
	}
	for _, r := range fraction {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid amount")
		}
	}
	fraction += strings.Repeat("0", 6-len(fraction))
	combined := strings.TrimLeft(whole+fraction, "0")
	if combined == "" {
		return 0, nil
	}
	integer, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return 0, fmt.Errorf("invalid amount")
	}
	if negative {
		integer.Neg(integer)
	}
	if !integer.IsInt64() {
		return 0, fmt.Errorf("amount is out of range")
	}
	return integer.Int64(), nil
}

func MicrosFromFloat(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("amount must be finite")
	}
	return ParseAmountMicros(strconv.FormatFloat(value, 'f', 6, 64))
}

func FormatAmountMicros(value int64) string {
	negative := value < 0
	abs := value
	if negative {
		if value == math.MinInt64 {
			whole := new(big.Int).SetInt64(value)
			whole.Abs(whole)
			q, r := new(big.Int), new(big.Int)
			q.QuoRem(whole, big.NewInt(MicrosPerUnit), r)
			return fmt.Sprintf("-%s.%06d", q.String(), r.Int64())
		}
		abs = -value
	}
	prefix := ""
	if negative {
		prefix = "-"
	}
	return fmt.Sprintf("%s%d.%06d", prefix, abs/MicrosPerUnit, abs%MicrosPerUnit)
}

func ConvertMicros(amount int64, from, to string, rates map[string]string) (int64, error) {
	from, fromOK := NormalizeCurrency(from)
	to, toOK := NormalizeCurrency(to)
	if !fromOK || !toOK {
		return 0, fmt.Errorf("unsupported currency")
	}
	if from == to {
		return amount, nil
	}
	fromRate, ok := new(big.Rat).SetString(rates[from])
	if !ok || fromRate.Sign() <= 0 {
		return 0, fmt.Errorf("missing rate for %s", from)
	}
	toRate, ok := new(big.Rat).SetString(rates[to])
	if !ok || toRate.Sign() <= 0 {
		return 0, fmt.Errorf("missing rate for %s", to)
	}
	result := new(big.Rat).SetInt64(amount)
	result.Mul(result, toRate)
	result.Quo(result, fromRate)
	return roundRatToInt64(result)
}

func roundRatToInt64(value *big.Rat) (int64, error) {
	numerator := new(big.Int).Set(value.Num())
	denominator := new(big.Int).Set(value.Denom())
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	absRemainder := new(big.Int).Abs(remainder)
	if absRemainder.Lsh(absRemainder, 1).Cmp(denominator) >= 0 {
		if numerator.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("converted amount is out of range")
	}
	return quotient.Int64(), nil
}
