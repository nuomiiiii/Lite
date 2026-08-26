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
	return &snapshot, nil
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
	return rates, nil
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
