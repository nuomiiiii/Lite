package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

const (
	DefaultFXEndpoint = "https://api.frankfurter.app/latest?from=USD"
	FXProvider        = "frankfurter"
	maxFXBodyBytes    = 1 << 20
)

// RequiredFXCurrencies are stored on every snapshot. Display is only CNY or USD,
// so EUR/GBP/CAD quotes stay in the same snapshot and convert in one ratio
// (native → CNY or native → USD), not by turning the amount into USD first.
var RequiredFXCurrencies = []string{"USD", "CNY", "EUR", "GBP", "CAD"}

var isoCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

type FXState struct {
	Status    string     `json:"status"`
	Provider  string     `json:"provider,omitempty"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
}

func ParseRatesJSON(raw string) (map[string]string, error) {
	rates := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &rates); err != nil {
		return nil, fmt.Errorf("decode rates: %w", err)
	}
	for currency, rate := range rates {
		if !isoCurrencyPattern.MatchString(currency) {
			return nil, fmt.Errorf("invalid currency %q", currency)
		}
		parsed, ok := new(big.Rat).SetString(rate)
		if !ok || parsed.Sign() <= 0 {
			return nil, fmt.Errorf("invalid rate for %s", currency)
		}
	}
	if rates["USD"] == "" {
		return nil, fmt.Errorf("USD base rate is missing")
	}
	return rates, nil
}

func LatestFXSnapshot(db *gorm.DB) (*models.BillingFXSnapshot, map[string]string, error) {
	var snapshot models.BillingFXSnapshot
	if err := db.Order("fetched_at DESC, id DESC").First(&snapshot).Error; err != nil {
		return nil, nil, err
	}
	rates, err := ParseRatesJSON(snapshot.RatesJSON)
	if err != nil {
		return nil, nil, err
	}
	return &snapshot, rates, nil
}

func FXStatus(snapshot *models.BillingFXSnapshot, now time.Time) FXState {
	if snapshot == nil {
		return FXState{Status: "unavailable"}
	}
	fetchedAt := snapshot.FetchedAt.UTC()
	age := now.UTC().Sub(fetchedAt)
	status := "latest"
	if age > 7*24*time.Hour {
		status = "expired"
	} else if age > 72*time.Hour {
		status = "cached"
	}
	return FXState{Status: status, Provider: snapshot.Provider, FetchedAt: &fetchedAt}
}

func RefreshFX(ctx context.Context, db *gorm.DB, httpClient *http.Client, endpoint string) (*models.BillingFXSnapshot, error) {
	if endpoint == "" {
		endpoint = DefaultFXEndpoint
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch reference rates: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch reference rates: HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, maxFXBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read reference rates: %w", err)
	}
	if len(body) > maxFXBodyBytes {
		return nil, fmt.Errorf("reference-rate response exceeds 1 MB")
	}
	rates, err := parseFXResponse(body)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(rates)
	if err != nil {
		return nil, fmt.Errorf("encode reference rates: %w", err)
	}
	now := time.Now().UTC()
	snapshot := models.BillingFXSnapshot{
		Provider:     FXProvider,
		BaseCurrency: "USD",
		RatesJSON:    string(encoded),
		FetchedAt:    now,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		return nil, fmt.Errorf("save reference rates: %w", err)
	}
	if err := BackfillPriceVersionFX(db); err != nil {
		return &snapshot, fmt.Errorf("backfill billing FX after refresh: %w", err)
	}
	return &snapshot, nil
}

// ApplyUpgradeFX locks the current rate onto any price version that never had one.
// Versions that already stored a snapshot keep it. A fetch failure still stamps
// whatever snapshot is already in the database.
func ApplyUpgradeFX(ctx context.Context, db *gorm.DB, httpClient *http.Client, endpoint string) error {
	fetch, err := shouldFetchUpgradeFX(db)
	if err != nil {
		return err
	}
	if fetch {
		if _, refreshErr := RefreshFX(ctx, db, httpClient, endpoint); refreshErr == nil {
			return nil
		}
	}
	return BackfillPriceVersionFX(db)
}

func shouldFetchUpgradeFX(db *gorm.DB) (bool, error) {
	var versions []models.BillingPriceVersion
	if err := db.Where("fx_snapshot_id IS NULL AND price_micros > 0 AND currency_valid = ?", true).
		Find(&versions).Error; err != nil {
		return false, fmt.Errorf("list billing versions missing FX snapshots: %w", err)
	}
	if len(versions) == 0 {
		return false, nil
	}
	_, rates, err := LatestFXSnapshot(db)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return true, nil
		}
		return false, err
	}
	for _, version := range versions {
		if _, convErr := ConvertMicros(version.PriceMicros, version.Currency, "USD", rates); convErr == nil {
			return true, nil
		}
	}
	return false, nil
}

func parseFXResponse(body []byte) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload struct {
		Base  string                     `json:"base"`
		Rates map[string]json.RawMessage `json:"rates"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode reference rates: %w", err)
	}
	if strings.ToUpper(strings.TrimSpace(payload.Base)) != "USD" || len(payload.Rates) == 0 {
		return nil, fmt.Errorf("reference-rate response has an invalid base or empty rates")
	}
	rates := map[string]string{"USD": "1"}
	keys := make([]string, 0, len(payload.Rates))
	for key := range payload.Rates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, rawCurrency := range keys {
		currency := strings.ToUpper(strings.TrimSpace(rawCurrency))
		if !isoCurrencyPattern.MatchString(currency) {
			return nil, fmt.Errorf("reference-rate response contains invalid currency %q", rawCurrency)
		}
		var number json.Number
		if err := json.Unmarshal(payload.Rates[rawCurrency], &number); err != nil {
			return nil, fmt.Errorf("reference-rate response contains invalid rate for %s", currency)
		}
		rate := number.String()
		parsed, ok := new(big.Rat).SetString(rate)
		if !ok || parsed.Sign() <= 0 {
			return nil, fmt.Errorf("reference-rate response contains invalid rate for %s", currency)
		}
		rates[currency] = rate
	}
	if missing := missingRequiredRates(rates); len(missing) > 0 {
		return nil, fmt.Errorf("reference-rate response is missing %s", strings.Join(missing, ", "))
	}
	return rates, nil
}

func missingRequiredRates(rates map[string]string) []string {
	missing := make([]string, 0, len(RequiredFXCurrencies))
	for _, currency := range RequiredFXCurrencies {
		parsed, ok := new(big.Rat).SetString(rates[currency])
		if !ok || parsed.Sign() <= 0 {
			missing = append(missing, currency)
		}
	}
	return missing
}

func loadFXSnapshotsByIDs(ctx context.Context, db *gorm.DB, ids []uint64) (map[uint64]map[string]string, error) {
	snapshots := map[uint64]map[string]string{}
	if len(ids) == 0 {
		return snapshots, nil
	}
	var rows []models.BillingFXSnapshot
	if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		rates, err := ParseRatesJSON(row.RatesJSON)
		if err == nil {
			snapshots[row.ID] = rates
		}
	}
	return snapshots, nil
}

func loadVersionFXSnapshots(ctx context.Context, db *gorm.DB, versions []models.BillingPriceVersion) (map[uint64]map[string]string, error) {
	seen := map[uint64]struct{}{}
	ids := make([]uint64, 0, len(versions))
	for _, version := range versions {
		if version.FXSnapshotID == nil {
			continue
		}
		if _, ok := seen[*version.FXSnapshotID]; ok {
			continue
		}
		seen[*version.FXSnapshotID] = struct{}{}
		ids = append(ids, *version.FXSnapshotID)
	}
	return loadFXSnapshotsByIDs(ctx, db, ids)
}

func SnapshotConversion(db *gorm.DB, amountMicros int64, currency string) (*uint64, *int64, error) {
	snapshot, rates, err := LatestFXSnapshot(db)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	usd, err := ConvertMicros(amountMicros, currency, "USD", rates)
	if err != nil {
		return nil, nil, nil
	}
	id := snapshot.ID
	return &id, &usd, nil
}
