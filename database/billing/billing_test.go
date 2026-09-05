package billing

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func billingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.BillingPriceVersion{},
		&models.BillingFXSnapshot{},
		&models.BillingEntry{},
	))
	return db
}

func beijingTime(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, BeijingLocation)
}

func yearMonthKeys(year int) []string {
	keys := make([]string, 0, 12)
	for month := 1; month <= 12; month++ {
		keys = append(keys, fmt.Sprintf("%04d-%02d", year, month))
	}
	return keys
}

func saveFX(t *testing.T, db *gorm.DB, fetchedAt time.Time, cny, cad string) models.BillingFXSnapshot {
	t.Helper()
	snapshot := models.BillingFXSnapshot{
		Provider: FXProvider, BaseCurrency: "USD",
		RatesJSON: fmt.Sprintf(`{"CAD":"%s","CNY":"%s","EUR":"0.92","GBP":"0.78","USD":"1"}`, cad, cny),
		FetchedAt: fetchedAt.UTC(),
	}
	require.NoError(t, db.Create(&snapshot).Error)
	return snapshot
}

func saveClient(t *testing.T, db *gorm.DB, client models.Client) models.Client {
	t.Helper()
	if client.UUID == "" {
		client.UUID = uuid.NewString()
	}
	if client.Token == "" {
		client.Token = uuid.NewString()
	}
	require.NoError(t, db.Create(&client).Error)
	return client
}

func TestCurrencyUsesExactMicrosAndCrossRates(t *testing.T) {
	rates := map[string]string{"USD": "1", "CNY": "7.2", "CAD": "1.35"}
	usd, err := ConvertMicros(7_200_000, "CNY", "USD", rates)
	require.NoError(t, err)
	assert.Equal(t, int64(1_000_000), usd)
	cad, err := ConvertMicros(7_200_000, "CNY", "CAD", rates)
	require.NoError(t, err)
	assert.Equal(t, int64(1_350_000), cad)
	cny, err := ConvertMicros(1_350_000, "CAD", "CNY", rates)
	require.NoError(t, err)
	assert.Equal(t, int64(7_200_000), cny)
	minimum := strconvInt64(t, "-9223372036854.775808")
	assert.Equal(t, int64(math.MinInt64), minimum)
	assert.Equal(t, "-9223372036854.775808", FormatAmountMicros(minimum))
	_, err = ParseAmountMicros("1.0000001")
	assert.Error(t, err)
}

func TestNormalizeCurrencyUsesISORegistry(t *testing.T) {
	for _, value := range []string{"USD", "eur", "JPY"} {
		normalized, valid := NormalizeCurrency(value)
		assert.True(t, valid, value)
		assert.Equal(t, strings.ToUpper(value), normalized)
	}
	for _, tc := range []struct{ in, want string }{
		{"¥", "CNY"},
		{"$", "USD"},
		{"€", "EUR"},
		{"£", "GBP"},
		{"C$", "CAD"},
		{"CAD", "CAD"},
	} {
		normalized, valid := NormalizeCurrency(tc.in)
		assert.True(t, valid, tc.in)
		assert.Equal(t, tc.want, normalized, tc.in)
	}
	for _, value := range []string{"XYZ", "ABC", "12A"} {
		_, valid := NormalizeCurrency(value)
		assert.False(t, valid, value)
	}
}

func TestReconcileStoredCurrencySymbols(t *testing.T) {
	db := billingTestDB(t)
	saveFX(t, db, time.Now().UTC(), "7.2", "1.35")
	euro := saveClient(t, db, models.Client{Name: "eu", Price: 23.33, BillingCycle: 365, Currency: "€"})
	require.NoError(t, db.Create(&models.BillingPriceVersion{
		Client: euro.UUID, ClientName: euro.Name, PriceMicros: 23_330_000,
		Currency: "€", CurrencyValid: false, BillingCycleDays: 365,
		EffectiveFrom: time.Now().UTC(), Source: PriceSourceMigration,
	}).Error)
	pound := saveClient(t, db, models.Client{Name: "uk", Price: 1.99, BillingCycle: 30, Currency: "£"})
	require.NoError(t, db.Create(&models.BillingPriceVersion{
		Client: pound.UUID, ClientName: pound.Name, PriceMicros: 1_990_000,
		Currency: "£", CurrencyValid: false, BillingCycleDays: 30,
		EffectiveFrom: time.Now().UTC(), Source: PriceSourceMigration,
	}).Error)
	require.NoError(t, ReconcileStoredCurrencies(db))
	require.NoError(t, BackfillPriceVersionFX(db))

	var euroVersion, poundVersion models.BillingPriceVersion
	require.NoError(t, db.Where("client = ?", euro.UUID).First(&euroVersion).Error)
	require.NoError(t, db.Where("client = ?", pound.UUID).First(&poundVersion).Error)
	assert.Equal(t, "EUR", euroVersion.Currency)
	assert.True(t, euroVersion.CurrencyValid)
	require.NotNil(t, euroVersion.FXSnapshotID)
	assert.Equal(t, "GBP", poundVersion.Currency)
	assert.True(t, poundVersion.CurrencyValid)
	require.NotNil(t, poundVersion.FXSnapshotID)
}

func strconvInt64(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := ParseAmountMicros(value)
	require.NoError(t, err)
	return parsed
}

func TestHistoricalEntryKeepsItsFXSnapshot(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 12, 0)
	first := saveFX(t, db, now.Add(-time.Hour), "7", "1.3")
	client := saveClient(t, db, models.Client{Name: "snapshot-node", Currency: "USD"})
	entry, err := CreateTrafficResetEntry(context.Background(), db, TrafficResetInput{Client: client.UUID, Amount: "1", Currency: "USD", OccurredAt: now, IdempotencyKey: "snapshot-fixed"})
	require.NoError(t, err)
	require.NotNil(t, entry.FXSnapshotID)
	assert.Equal(t, first.ID, *entry.FXSnapshotID)
	saveFX(t, db, now, "8", "1.4")
	overview, err := GetOverview(context.Background(), db, "CNY", now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "7.000000", overview.Summary.Today.Extra)
}

func TestPriceVersionChangeDoesNotRewriteOldAccrual(t *testing.T) {
	db := billingTestDB(t)
	start := beijingTime(2026, time.August, 1, 0, 0)
	client := saveClient(t, db, models.Client{Name: "versioned", Price: 30, BillingCycle: 30, Currency: "CNY"})
	require.NoError(t, EnsureInitialPriceVersions(db, start))
	require.NoError(t, EnsureAccruedThrough(context.Background(), db, start))
	var first models.BillingEntry
	require.NoError(t, db.Where("client = ? AND day = ?", client.UUID, "2026-08-01").First(&first).Error)
	assert.Equal(t, int64(1_000_000), first.OriginalAmountMicros)

	changeAt := beijingTime(2026, time.August, 2, 0, 0)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var existing models.Client
		if err := tx.First(&existing, "uuid = ?", client.UUID).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{"price": float64(60)}
		if err := CapturePriceVersion(tx, existing, updates, PriceSourceClientEdit, changeAt); err != nil {
			return err
		}
		return tx.Model(&models.Client{}).Where("uuid = ?", client.UUID).Updates(updates).Error
	}))
	require.NoError(t, EnsureAccruedThrough(context.Background(), db, changeAt))
	var oldAfter, second models.BillingEntry
	require.NoError(t, db.First(&oldAfter, first.ID).Error)
	require.NoError(t, db.Where("client = ? AND day = ?", client.UUID, "2026-08-02").First(&second).Error)
	assert.Equal(t, int64(1_000_000), oldAfter.OriginalAmountMicros)
	assert.Equal(t, int64(2_000_000), second.OriginalAmountMicros)
}

func TestTrafficResetAppearsInDayMonthYearAndIsIdempotent(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 10, 0)
	saveFX(t, db, now.Add(-time.Hour), "7", "1.3")
	client := saveClient(t, db, models.Client{Name: "traffic", Currency: "USD"})
	input := TrafficResetInput{Client: client.UUID, Amount: "5.00", Currency: "USD", OccurredAt: now, IdempotencyKey: "same-request"}
	first, err := CreateTrafficResetEntry(context.Background(), db, input)
	require.NoError(t, err)
	second, err := CreateTrafficResetEntry(context.Background(), db, input)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	var count int64
	require.NoError(t, db.Model(&models.BillingEntry{}).Where("entry_key = ?", first.EntryKey).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	overview, err := GetOverview(context.Background(), db, "USD", now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "5.000000", overview.Summary.Today.Extra)
	monthly, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "USD", Now: now.Add(time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, "5.000000", monthly.Summary.Extra)
	yearly, err := GetYearly(context.Background(), db, PeriodQuery{Currency: "USD", Now: now.Add(time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, "5.000000", yearly.Summary.Extra)
}

func TestVoidCreatesOriginalPeriodReversalAndKeepsOriginal(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 10, 0)
	client := saveClient(t, db, models.Client{Name: "void", Currency: "CNY"})
	original, err := CreateTrafficResetEntry(context.Background(), db, TrafficResetInput{Client: client.UUID, Amount: "35.9", Currency: "CNY", OccurredAt: now, IdempotencyKey: "void-me"})
	require.NoError(t, err)
	reversal, err := VoidEntry(context.Background(), db, original.ID, "wrong amount", "operator")
	require.NoError(t, err)
	assert.Equal(t, original.Day, reversal.Day)
	assert.Equal(t, -original.OriginalAmountMicros, reversal.OriginalAmountMicros)
	assert.Equal(t, &original.ID, reversal.ReversalOf)
	var rows []models.BillingEntry
	require.NoError(t, db.Order("id").Find(&rows).Error)
	assert.Len(t, rows, 2)
	overview, err := GetOverview(context.Background(), db, "CNY", now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "0.000000", overview.Summary.Today.Extra)
	again, err := VoidEntry(context.Background(), db, original.ID, "retry", "operator")
	require.NoError(t, err)
	assert.Equal(t, reversal.ID, again.ID)
	page, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", Client: client.UUID, From: original.Day, To: original.Day, Page: 1, PageSize: 100, Now: now.Add(time.Minute),
	})
	require.NoError(t, err)
	var foundOriginal, foundReversal bool
	for _, row := range page.Items {
		if row.ID == original.ID {
			foundOriginal = true
			assert.False(t, row.Voidable)
			assert.True(t, row.Voided)
		}
		if row.ID == reversal.ID {
			foundReversal = true
			assert.False(t, row.Voidable)
			assert.False(t, row.Voided)
		}
	}
	assert.True(t, foundOriginal)
	assert.True(t, foundReversal)
	voidedPage, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", Client: client.UUID, From: original.Day, To: original.Day, Types: []string{"voided"}, Page: 1, PageSize: 100, Now: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Len(t, voidedPage.Items, 1)
	assert.Equal(t, original.ID, voidedPage.Items[0].ID)
	assert.True(t, voidedPage.Items[0].Voided)
	reversalPage, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", Client: client.UUID, From: original.Day, To: original.Day, Types: []string{"reversal"}, Page: 1, PageSize: 100, Now: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Len(t, reversalPage.Items, 1)
	assert.Equal(t, reversal.ID, reversalPage.Items[0].ID)
}

func TestBeijingBillingBoundaries(t *testing.T) {
	before := time.Date(2026, time.August, 24, 15, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	assert.Equal(t, "2026-08-24", BeijingDay(before).Format(time.DateOnly))
	assert.Equal(t, "2026-08-25", BeijingDay(after).Format(time.DateOnly))
	assert.Equal(t, "2024-02-29", BeijingDay(beijingTime(2024, time.February, 29, 23, 59)).Format(time.DateOnly))
	assert.Equal(t, "2025-01-01", BeijingDay(beijingTime(2025, time.January, 1, 0, 0)).Format(time.DateOnly))
}

func TestSpecialPricesAndOneTimeEntry(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 10, 0)
	zero := saveClient(t, db, models.Client{Name: "zero", Price: 0, BillingCycle: 30, Currency: "USD"})
	free := saveClient(t, db, models.Client{Name: "free", Price: -1, BillingCycle: 30, Currency: "USD"})
	oneTime := saveClient(t, db, models.Client{Name: "once", Price: 0, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, EnsureInitialPriceVersions(db, now))
	require.NoError(t, EnsureAccruedThrough(context.Background(), db, BeijingDay(now)))
	var baseCount int64
	require.NoError(t, db.Model(&models.BillingEntry{}).Where("type = ? AND client IN ?", EntryTypeBaseAccrual, []string{zero.UUID, free.UUID}).Count(&baseCount).Error)
	assert.Zero(t, baseCount)
	var existing models.Client
	require.NoError(t, db.First(&existing, "uuid = ?", oneTime.UUID).Error)
	updates := map[string]interface{}{"price": float64(12.5), "billing_cycle": float64(-1)}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return CapturePriceVersion(tx, existing, updates, PriceSourceClientEdit, now.Add(time.Minute))
	}))
	var oneTimeCount int64
	require.NoError(t, db.Model(&models.BillingEntry{}).Where("client = ? AND type = ?", oneTime.UUID, EntryTypeOneTime).Count(&oneTimeCount).Error)
	assert.Equal(t, int64(1), oneTimeCount)
}

func TestAccrualExpiryNoExpiryAndRenewal(t *testing.T) {
	db := billingTestDB(t)
	day := beijingTime(2026, time.August, 1, 0, 0)
	noExpiry := models.BillingPriceVersion{Client: "open", ClientName: "open", PriceMicros: 30_000_000, Currency: "CNY", CurrencyValid: true, BillingCycleDays: 30, EffectiveFrom: day, Source: PriceSourceMigration}
	noon := day.Add(12 * time.Hour)
	halfExpiry := models.BillingPriceVersion{Client: "half", ClientName: "half", PriceMicros: 30_000_000, Currency: "CNY", CurrencyValid: true, BillingCycleDays: 30, EffectiveFrom: day, ExpiredAt: &noon, Source: PriceSourceMigration}
	require.NoError(t, db.Create(&noExpiry).Error)
	require.NoError(t, db.Create(&halfExpiry).Error)
	require.NoError(t, EnsureAccruedThrough(context.Background(), db, day))
	var openEntry, halfEntry models.BillingEntry
	require.NoError(t, db.Where("client = ?", "open").First(&openEntry).Error)
	require.NoError(t, db.Where("client = ?", "half").First(&halfEntry).Error)
	assert.Equal(t, int64(1_000_000), openEntry.OriginalAmountMicros)
	assert.Equal(t, int64(500_000), halfEntry.OriginalAmountMicros)

	client := saveClient(t, db, models.Client{UUID: "renewed", Name: "renewed", Price: 30, BillingCycle: 30, Currency: "CNY", ExpiredAt: &noon})
	require.NoError(t, EnsureInitialPriceVersions(db, day))
	newExpiry := day.AddDate(0, 0, 30)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{"expired_at": newExpiry}
		if err := CapturePriceVersion(tx, client, updates, PriceSourceRenewal, noon); err != nil {
			return err
		}
		return tx.Model(&models.Client{}).Where("uuid = ?", client.UUID).Update("expired_at", newExpiry).Error
	}))
	var renewed models.BillingPriceVersion
	require.NoError(t, db.Where("client = ? AND effective_to IS NULL", client.UUID).First(&renewed).Error)
	assert.Equal(t, PriceSourceRenewal, renewed.Source)
	require.NoError(t, EnsureAccruedThrough(context.Background(), db, day))
	var renewedEntries []models.BillingEntry
	require.NoError(t, db.Where("client = ? AND day = ?", client.UUID, "2026-08-01").Find(&renewedEntries).Error)
	assert.Len(t, renewedEntries, 2)
}

func TestCurrentDayAccrualPostsFullDailyShare(t *testing.T) {
	db := billingTestDB(t)
	day := beijingTime(2026, time.August, 26, 0, 0)
	noon := day.Add(12 * time.Hour)
	version := models.BillingPriceVersion{
		Client: "full-day", ClientName: "full-day", PriceMicros: 30_000_000, Currency: "CNY",
		CurrencyValid: true, BillingCycleDays: 30, EffectiveFrom: day, Source: PriceSourceMigration,
	}
	require.NoError(t, db.Create(&version).Error)
	entries, err := CurrentDayAccruals(context.Background(), db, noon)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, int64(1_000_000), entries[0].OriginalAmountMicros)
	assert.Equal(t, "2026-08-26", entries[0].Day)
}

func TestCreateIPChangeEntry(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 26, 12, 0)
	saveFX(t, db, now.Add(-time.Hour), "7", "1.3")
	client := saveClient(t, db, models.Client{Name: "ip-node", Currency: "USD"})
	entry, err := CreateIPChangeEntry(context.Background(), db, TrafficResetInput{
		Client: client.UUID, Amount: "2", Currency: "USD", IdempotencyKey: "ip-1",
		OccurredAt: now,
	})
	require.NoError(t, err)
	assert.Equal(t, EntryTypeIPChange, entry.Type)
	assert.Equal(t, int64(2_000_000), entry.OriginalAmountMicros)
	assert.Equal(t, "2026-08-26", entry.Day)

	overview, err := GetOverview(context.Background(), db, "USD", now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "2.000000", overview.Summary.Today.Other)
	assert.Equal(t, "0.000000", overview.Summary.Today.Extra)
	assert.Equal(t, "2.000000", overview.MonthComposition.Other)
}

func TestCreateOneTimeFeeEntry(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 26, 12, 0)
	saveFX(t, db, now.Add(-time.Hour), "7", "1.3")
	client := saveClient(t, db, models.Client{Name: "one-time-node", Currency: "USD"})
	entry, err := CreateOneTimeFeeEntry(context.Background(), db, TrafficResetInput{
		Client: client.UUID, Amount: "3.5", Currency: "USD", IdempotencyKey: "fee-1",
		OccurredAt: now, Note: "线路升级补差",
	})
	require.NoError(t, err)
	assert.Equal(t, EntryTypeAdjustment, entry.Type)
	assert.Equal(t, int64(3_500_000), entry.OriginalAmountMicros)
	assert.Equal(t, "线路升级补差", entry.Note)
	assert.Equal(t, "2026-08-26", entry.Day)

	overview, err := GetOverview(context.Background(), db, "USD", now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "3.500000", overview.Summary.Today.OneTime)
	assert.Equal(t, "0.000000", overview.Summary.Today.Extra)
	assert.Equal(t, "3.500000", overview.MonthComposition.OneTime)

	page, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "USD", Client: client.UUID, Types: []string{EntryTypeAdjustment}, Page: 1, PageSize: 10, Now: now.Add(time.Minute),
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].Voidable)
	assert.Equal(t, "线路升级补差", page.Items[0].Note)
}

func TestCommittedMonthlyUsesTrafficResetDayAndStopsAtExpiry(t *testing.T) {
	db := billingTestDB(t)
	resetDay := 15
	start := beijingTime(2026, time.August, 1, 0, 0)
	expiry := beijingTime(2027, time.April, 22, 0, 0)
	client := saveClient(t, db, models.Client{
		Name: "cycle-node", Price: 365, BillingCycle: 365, Currency: "CNY",
		TrafficResetDay: &resetDay, ExpiredAt: &expiry,
	})
	require.NoError(t, EnsureInitialPriceVersions(db, start))
	var version models.BillingPriceVersion
	require.NoError(t, db.Where("client = ?", client.UUID).First(&version).Error)
	_, monthlyNative, _, err := cycleAverageMicros(version)
	require.NoError(t, err)
	expected := FormatAmountMicros(monthlyNative)

	early, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Months: yearMonthKeys(2026), Now: beijingTime(2026, time.August, 16, 12, 0), PageSize: 20})
	require.NoError(t, err)
	late, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Months: yearMonthKeys(2026), Now: beijingTime(2026, time.August, 26, 12, 0), PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, expected, periodByKey(early.Items, "2026-08").Base)
	assert.Equal(t, periodByKey(early.Items, "2026-08").Base, periodByKey(late.Items, "2026-08").Base)
	assert.Equal(t, "projected", periodByKey(late.Items, "2026-09").Status)
	assert.Equal(t, expected, periodByKey(late.Items, "2026-09").Base)

	nextYear, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Months: yearMonthKeys(2027), Now: beijingTime(2026, time.August, 26, 12, 0), PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, expected, periodByKey(nextYear.Items, "2027-04").Base)
	assert.Equal(t, "no_record", periodByKey(nextYear.Items, "2027-05").Status)
	assert.Equal(t, "0.000000", periodByKey(nextYear.Items, "2027-05").Base)
}

func periodByKey(items []PeriodAmount, key string) PeriodAmount {
	for _, item := range items {
		if item.Period == key {
			return item
		}
	}
	return PeriodAmount{}
}

func TestFXValidationTimeoutAndCacheFallback(t *testing.T) {
	db := billingTestDB(t)
	cached := saveFX(t, db, time.Now().Add(-time.Hour), "7", "1.3")
	tests := []struct {
		name, body string
		wait       bool
	}{
		{name: "invalid json", body: `{`},
		{name: "negative rate", body: `{"base":"USD","rates":{"CNY":-7}}`},
		{name: "missing euro", body: `{"base":"USD","rates":{"CAD":1.35,"CNY":7.2}}`},
		{name: "timeout", wait: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.wait {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := &http.Client{Timeout: 20 * time.Millisecond}
			_, err := RefreshFX(context.Background(), db, client, server.URL)
			assert.Error(t, err)
			latest, _, latestErr := LatestFXSnapshot(db)
			require.NoError(t, latestErr)
			assert.Equal(t, cached.ID, latest.ID)
		})
	}
}

func TestMonthlyDefaultsCurrentMonthAndSelectedMonthsSortAndPage(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 12, 0)
	client := saveClient(t, db, models.Client{Name: "periods", Currency: "CNY"})
	for index, day := range []string{"2024-12-31", "2025-12-31", "2026-01-02"} {
		entry := models.BillingEntry{EntryKey: fmt.Sprintf("period-%d", index), Client: client.UUID, ClientName: client.Name, Type: EntryTypeTrafficReset, Day: day, OccurredAt: now.Add(time.Duration(index) * time.Minute), OriginalAmountMicros: 1_000_000, OriginalCurrency: "CNY"}
		require.NoError(t, db.Create(&entry).Error)
	}
	defaultPage, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Now: now, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, defaultPage.Items, 1)
	assert.Equal(t, "2026-08", defaultPage.Items[0].Period)
	assert.Equal(t, defaultPage.Items[0].Total, defaultPage.MonthlyAverage)

	yearly, err := GetYearly(context.Background(), db, PeriodQuery{Currency: "CNY", Years: []int{2026}, Now: now, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, yearly.Items, 1)
	assert.Equal(t, yearly.Items[0].Total, yearly.YearlyAverage)

	multi, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Months: []string{"2026-12", "2026-01"}, Now: now, Page: 1, PageSize: 2})
	require.NoError(t, err)
	require.Len(t, multi.Items, 2)
	assert.Equal(t, "2026-12", multi.Items[0].Period)
	assert.Equal(t, "2026-01", multi.Items[1].Period)
	assert.Equal(t, int64(2), multi.Page.Total)
}

func TestOverviewTrendUsesCurrentYearMonths(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.January, 31, 12, 0)
	overview, err := GetOverview(context.Background(), db, "CNY", now)
	require.NoError(t, err)

	periods := make([]string, 0, len(overview.MonthlyTrend))
	for _, item := range overview.MonthlyTrend {
		periods = append(periods, item.Period)
	}
	assert.Equal(t, []string{
		"2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06",
		"2026-07", "2026-08", "2026-09", "2026-10", "2026-11", "2026-12",
	}, periods)
}

func TestBackfillStampsCurrentFXForMigratedVersions(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 26, 12, 0)
	expiry := now.AddDate(0, 0, 15)
	usd := saveClient(t, db, models.Client{
		Name: "US-01", Price: 30, BillingCycle: 30, Currency: "USD", ExpiredAt: &expiry,
	})
	unsupported := saveClient(t, db, models.Client{
		Name: "XX-01", Price: 10, BillingCycle: 30, Currency: "KRW",
	})
	require.NoError(t, db.Create(&models.BillingPriceVersion{
		Client: unsupported.UUID, ClientName: unsupported.Name, PriceMicros: 10_000_000,
		Currency: "KRW", CurrencyValid: true, BillingCycleDays: 30,
		EffectiveFrom: now, Source: PriceSourceMigration,
	}).Error)
	require.NoError(t, db.Create(&models.BillingPriceVersion{
		Client: usd.UUID, ClientName: usd.Name, PriceMicros: 30_000_000,
		Currency: "USD", CurrencyValid: true, BillingCycleDays: 30,
		ExpiredAt: &expiry, EffectiveFrom: now, Source: PriceSourceMigration,
	}).Error)

	snapshot := saveFX(t, db, now, "7.2", "1.35")
	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	byName := map[string]BillingServerRow{}
	for _, row := range page.Items {
		byName[row.Name] = row
	}

	var usdVersion models.BillingPriceVersion
	require.NoError(t, db.Where("client = ?", usd.UUID).First(&usdVersion).Error)
	require.NotNil(t, usdVersion.FXSnapshotID)
	assert.Equal(t, snapshot.ID, *usdVersion.FXSnapshotID)
	require.NotNil(t, usdVersion.USDPriceMicros)
	assert.Equal(t, int64(30_000_000), *usdVersion.USDPriceMicros)
	require.NotNil(t, byName["US-01"].DailyAverage)
	assert.Equal(t, "7.200000", *byName["US-01"].DailyAverage)
	require.NotNil(t, byName["US-01"].MonthlyAverage)
	assert.Equal(t, "216.000000", *byName["US-01"].MonthlyAverage)
	require.NotNil(t, byName["US-01"].RemainingValue)

	var skipped models.BillingPriceVersion
	require.NoError(t, db.Where("client = ?", unsupported.UUID).First(&skipped).Error)
	assert.Nil(t, skipped.FXSnapshotID)
}

func fxServer(t *testing.T, cny string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"base":"USD","rates":{"CAD":1.35,"CNY":%s,"EUR":0.92,"GBP":0.78}}`, cny)))
	}))
	t.Cleanup(server.Close)
	return server
}

func serverAverages(t *testing.T, rows []BillingServerRow) map[string]BillingServerRow {
	t.Helper()
	byName := map[string]BillingServerRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	return byName
}

func TestUpgradeFromPreBillingLocksFXAtUpgrade(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.September, 2, 12, 0)
	saveClient(t, db, models.Client{Name: "legacy-usd", Price: 30, BillingCycle: 30, Currency: "USD"})
	saveClient(t, db, models.Client{Name: "legacy-cny", Price: 72, BillingCycle: 30, Currency: "CNY"})
	saveClient(t, db, models.Client{Name: "legacy-eur", Price: 9.20, BillingCycle: 30, Currency: "EUR"})
	saveClient(t, db, models.Client{Name: "legacy-cad", Price: 13.50, BillingCycle: 30, Currency: "CAD"})
	saveClient(t, db, models.Client{Name: "legacy-gbp", Price: 7.80, BillingCycle: 30, Currency: "GBP"})
	require.NoError(t, EnsureInitialPriceVersions(db, now))
	require.NoError(t, ReconcileStoredCurrencies(db))

	var unlocked int64
	require.NoError(t, db.Model(&models.BillingPriceVersion{}).Where("fx_snapshot_id IS NULL").Count(&unlocked).Error)
	assert.Equal(t, int64(5), unlocked)

	require.NoError(t, ApplyUpgradeFX(context.Background(), db, nil, fxServer(t, "7.2").URL))

	cnyPage, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	cnyRows := serverAverages(t, cnyPage.Items)
	require.NotNil(t, cnyRows["legacy-usd"].DailyAverage)
	assert.Equal(t, "7.200000", *cnyRows["legacy-usd"].DailyAverage)
	require.NotNil(t, cnyRows["legacy-usd"].MonthlyAverage)
	assert.Equal(t, "216.000000", *cnyRows["legacy-usd"].MonthlyAverage)
	require.NotNil(t, cnyRows["legacy-usd"].YearlyAverage)
	assert.Equal(t, "2592.000000", *cnyRows["legacy-usd"].YearlyAverage)
	require.NotNil(t, cnyRows["legacy-cny"].MonthlyAverage)
	assert.Equal(t, "72.000000", *cnyRows["legacy-cny"].MonthlyAverage)
	for _, name := range []string{"legacy-eur", "legacy-cad", "legacy-gbp"} {
		require.NotNil(t, cnyRows[name].DailyAverage, name)
		require.NotNil(t, cnyRows[name].MonthlyAverage, name)
		require.NotNil(t, cnyRows[name].YearlyAverage, name)
		assert.Equal(t, "72.000000", *cnyRows[name].MonthlyAverage, name)
		assert.Equal(t, "864.000000", *cnyRows[name].YearlyAverage, name)
	}

	usdPage, err := GetServers(context.Background(), db, ServerQuery{Currency: "USD", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	usdRows := serverAverages(t, usdPage.Items)
	require.NotNil(t, usdRows["legacy-usd"].MonthlyAverage)
	assert.Equal(t, "30.000000", *usdRows["legacy-usd"].MonthlyAverage)
	require.NotNil(t, usdRows["legacy-cny"].MonthlyAverage)
	assert.Equal(t, "10.000000", *usdRows["legacy-cny"].MonthlyAverage)
	for _, name := range []string{"legacy-eur", "legacy-cad", "legacy-gbp"} {
		require.NotNil(t, usdRows[name].MonthlyAverage, name)
		assert.Equal(t, "10.000000", *usdRows[name].MonthlyAverage, name)
	}
}

func TestUpgradeFromBroken231LocksOnlyMissingFX(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.September, 2, 12, 0)
	old := saveFX(t, db, now.Add(-time.Hour), "7.2", "1.35")
	edited := saveClient(t, db, models.Client{Name: "already-locked", Price: 30, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, EnsureInitialPriceVersions(db, now.Add(-time.Hour)))
	broken := saveClient(t, db, models.Client{Name: "broken-231", Price: 30, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, db.Create(&models.BillingPriceVersion{
		Client: broken.UUID, ClientName: broken.Name, PriceMicros: 30_000_000,
		Currency: "USD", CurrencyValid: true, BillingCycleDays: 30,
		EffectiveFrom: now, Source: PriceSourceMigration,
	}).Error)

	require.NoError(t, ApplyUpgradeFX(context.Background(), db, nil, fxServer(t, "8.0").URL))

	var locked, migrated models.BillingPriceVersion
	require.NoError(t, db.Where("client = ?", edited.UUID).First(&locked).Error)
	require.NoError(t, db.Where("client = ?", broken.UUID).First(&migrated).Error)
	require.NotNil(t, locked.FXSnapshotID)
	assert.Equal(t, old.ID, *locked.FXSnapshotID)
	require.NotNil(t, migrated.FXSnapshotID)
	assert.NotEqual(t, old.ID, *migrated.FXSnapshotID)

	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	rows := serverAverages(t, page.Items)
	require.NotNil(t, rows["already-locked"].MonthlyAverage)
	assert.Equal(t, "216.000000", *rows["already-locked"].MonthlyAverage)
	require.NotNil(t, rows["broken-231"].MonthlyAverage)
	assert.Equal(t, "240.000000", *rows["broken-231"].MonthlyAverage)
}

func TestUpgradeFXFallsBackToCachedSnapshotWhenFetchFails(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.September, 2, 12, 0)
	cached := saveFX(t, db, now.Add(-time.Hour), "7.2", "1.35")
	usd := saveClient(t, db, models.Client{Name: "broken-offline", Price: 30, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, db.Create(&models.BillingPriceVersion{
		Client: usd.UUID, ClientName: usd.Name, PriceMicros: 30_000_000,
		Currency: "USD", CurrencyValid: true, BillingCycleDays: 30,
		EffectiveFrom: now, Source: PriceSourceMigration,
	}).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	require.NoError(t, ApplyUpgradeFX(context.Background(), db, nil, server.URL))
	var version models.BillingPriceVersion
	require.NoError(t, db.Where("client = ?", usd.UUID).First(&version).Error)
	require.NotNil(t, version.FXSnapshotID)
	assert.Equal(t, cached.ID, *version.FXSnapshotID)

	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.NotNil(t, page.Items[0].MonthlyAverage)
	assert.Equal(t, "216.000000", *page.Items[0].MonthlyAverage)
}

func TestEditBillingLocksFXAtSaveTime(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.September, 2, 12, 0)
	first := saveFX(t, db, now.Add(-time.Hour), "7.2", "1.35")
	client := saveClient(t, db, models.Client{Name: "edited", Price: 30, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, EnsureInitialPriceVersions(db, now.Add(-time.Hour)))
	second := saveFX(t, db, now, "8.0", "1.35")
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var existing models.Client
		if err := tx.First(&existing, "uuid = ?", client.UUID).Error; err != nil {
			return err
		}
		return CapturePriceVersion(tx, existing, map[string]interface{}{"price": float64(60)}, PriceSourceClientEdit, now)
	}))

	var versions []models.BillingPriceVersion
	require.NoError(t, db.Order("id").Where("client = ?", client.UUID).Find(&versions).Error)
	require.Len(t, versions, 2)
	require.NotNil(t, versions[0].FXSnapshotID)
	assert.Equal(t, first.ID, *versions[0].FXSnapshotID)
	require.NotNil(t, versions[0].EffectiveTo)
	require.NotNil(t, versions[1].FXSnapshotID)
	assert.Equal(t, second.ID, *versions[1].FXSnapshotID)

	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.NotNil(t, page.Items[0].MonthlyAverage)
	assert.Equal(t, "480.000000", *page.Items[0].MonthlyAverage)
}

func TestApplyUpgradeFXDoesNotRewriteLockedSnapshots(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.September, 2, 12, 0)
	first := saveFX(t, db, now.Add(-time.Hour), "7.2", "1.35")
	saveClient(t, db, models.Client{Name: "locked", Price: 30, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, EnsureInitialPriceVersions(db, now.Add(-time.Hour)))
	require.NoError(t, ApplyUpgradeFX(context.Background(), db, nil, fxServer(t, "8.0").URL))

	var snapshots []models.BillingFXSnapshot
	require.NoError(t, db.Order("id").Find(&snapshots).Error)
	require.Len(t, snapshots, 1)
	assert.Equal(t, first.ID, snapshots[0].ID)
	var version models.BillingPriceVersion
	require.NoError(t, db.First(&version).Error)
	require.NotNil(t, version.FXSnapshotID)
	assert.Equal(t, first.ID, *version.FXSnapshotID)
}

func TestLockedAveragesUseNativeRatesNotUSDHop(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.September, 2, 12, 0)
	snapshot := saveFX(t, db, now, "7.2", "1.35")
	wrongUSD := int64(1)
	version := models.BillingPriceVersion{
		PriceMicros: 9_200_000, Currency: "EUR", CurrencyValid: true,
		BillingCycleDays: 30, FXSnapshotID: &snapshot.ID, USDPriceMicros: &wrongUSD,
	}
	rates, err := ParseRatesJSON(snapshot.RatesJSON)
	require.NoError(t, err)
	snapshots := map[uint64]map[string]string{snapshot.ID: rates}

	cny, ok := convertLockedMicros(9_200_000, version, "CNY", snapshots)
	require.True(t, ok)
	assert.Equal(t, int64(72_000_000), cny)
	usd, ok := convertLockedMicros(9_200_000, version, "USD", snapshots)
	require.True(t, ok)
	assert.Equal(t, int64(10_000_000), usd)

	cad := version
	cad.Currency = "CAD"
	cad.PriceMicros = 13_500_000
	cadCNY, ok := convertLockedMicros(13_500_000, cad, "CNY", snapshots)
	require.True(t, ok)
	assert.Equal(t, int64(72_000_000), cadCNY)

	gbp := version
	gbp.Currency = "GBP"
	gbp.PriceMicros = 7_800_000
	gbpCNY, ok := convertLockedMicros(7_800_000, gbp, "CNY", snapshots)
	require.True(t, ok)
	assert.Equal(t, int64(72_000_000), gbpCNY)
}

func TestRefreshFXBackfillsVersionsMissingSnapshots(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 26, 12, 0)
	usd := saveClient(t, db, models.Client{Name: "US-02", Price: 30, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, db.Create(&models.BillingPriceVersion{
		Client: usd.UUID, ClientName: usd.Name, PriceMicros: 30_000_000,
		Currency: "USD", CurrencyValid: true, BillingCycleDays: 30,
		EffectiveFrom: now, Source: PriceSourceMigration,
	}).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"base":"USD","rates":{"CAD":1.35,"CNY":7.2,"EUR":0.92,"GBP":0.78}}`))
	}))
	defer server.Close()

	snapshot, err := RefreshFX(context.Background(), db, nil, server.URL)
	require.NoError(t, err)
	require.NotNil(t, snapshot)

	var version models.BillingPriceVersion
	require.NoError(t, db.Where("client = ?", usd.UUID).First(&version).Error)
	require.NotNil(t, version.FXSnapshotID)
	assert.Equal(t, snapshot.ID, *version.FXSnapshotID)
}

func TestRecurringFeesStayOnLockedFXWhileRemainingValueUsesLatest(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 26, 12, 0)
	expiry := now.AddDate(0, 0, 15)
	saveFX(t, db, now.Add(-time.Hour), "7.2", "1.35")
	saveClient(t, db, models.Client{
		Name: "TW-01", Price: 100, BillingCycle: 30, Currency: "USD", ExpiredAt: &expiry,
	})
	require.NoError(t, EnsureInitialPriceVersions(db, now.Add(-time.Hour)))

	before, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, before.Items, 1)
	require.NotNil(t, before.Items[0].MonthlyAverage)
	require.NotNil(t, before.Items[0].RemainingValue)
	lockedAverage := *before.Items[0].MonthlyAverage
	lockedRemaining := *before.Items[0].RemainingValue

	saveFX(t, db, now, "8.0", "1.35")
	after, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, after.Items, 1)
	require.NotNil(t, after.Items[0].MonthlyAverage)
	require.NotNil(t, after.Items[0].RemainingValue)
	assert.Equal(t, lockedAverage, *after.Items[0].MonthlyAverage)
	assert.NotEqual(t, lockedRemaining, *after.Items[0].RemainingValue)
}

func TestRemainingValueUsesFullRemainingTimePastOneCycle(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 27, 12, 0)
	expiry := now.Add(395 * 24 * time.Hour)
	saveClient(t, db, models.Client{
		Name: "Neburst_HK", Price: 383.04, BillingCycle: 365, Currency: "USD", ExpiredAt: &expiry,
	})
	require.NoError(t, EnsureInitialPriceVersions(db, now))

	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "USD", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.Items[0].RemainingDays)
	assert.Equal(t, 395, *page.Items[0].RemainingDays)
	require.NotNil(t, page.Items[0].RemainingValue)

	priceMicros, err := MicrosFromFloat(383.04)
	require.NoError(t, err)
	want, err := multiplyRatio(priceMicros, (395 * 24 * time.Hour).Nanoseconds(), (365 * 24 * time.Hour).Nanoseconds())
	require.NoError(t, err)
	got, err := ParseAmountMicros(*page.Items[0].RemainingValue)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Greater(t, got, priceMicros)

	value, days := remainingValue(models.BillingPriceVersion{
		PriceMicros: priceMicros, Currency: "USD", BillingCycleDays: 365, ExpiredAt: &expiry,
	}, "USD", nil, now)
	require.NotNil(t, value)
	require.NotNil(t, days)
	assert.Equal(t, 395, *days)
	direct, err := ParseAmountMicros(*value)
	require.NoError(t, err)
	assert.Equal(t, want, direct)
}

func TestRemainingValueIgnoresTrafficResetIPChangeAndOneTimeFees(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 27, 12, 0)
	expiry := now.Add(60 * 24 * time.Hour)
	client := saveClient(t, db, models.Client{
		Name: "remaining-base", Price: 30, BillingCycle: 30, Currency: "USD", ExpiredAt: &expiry,
	})
	require.NoError(t, EnsureInitialPriceVersions(db, now))
	saveFX(t, db, now.Add(-time.Hour), "7", "1.3")

	before, err := GetServers(context.Background(), db, ServerQuery{Currency: "USD", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, before.Items, 1)
	require.NotNil(t, before.Items[0].RemainingValue)
	beforeRemaining := *before.Items[0].RemainingValue
	beforeSummary, _, err := remainingValueSummary(context.Background(), db, "USD", now)
	require.NoError(t, err)

	_, err = CreateTrafficResetEntry(context.Background(), db, TrafficResetInput{Client: client.UUID, Amount: "8.00", Currency: "USD", OccurredAt: now, IdempotencyKey: "remain-reset"})
	require.NoError(t, err)
	_, err = CreateIPChangeEntry(context.Background(), db, TrafficResetInput{Client: client.UUID, Amount: "3.00", Currency: "USD", OccurredAt: now, IdempotencyKey: "remain-ip"})
	require.NoError(t, err)
	_, err = CreateOneTimeFeeEntry(context.Background(), db, TrafficResetInput{Client: client.UUID, Amount: "12.00", Currency: "USD", OccurredAt: now, IdempotencyKey: "remain-one-time"})
	require.NoError(t, err)

	after, err := GetServers(context.Background(), db, ServerQuery{Currency: "USD", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, after.Items, 1)
	require.NotNil(t, after.Items[0].RemainingValue)
	assert.Equal(t, beforeRemaining, *after.Items[0].RemainingValue)
	assert.Equal(t, "23.000000", after.Items[0].MonthExtra)
	afterSummary, _, err := remainingValueSummary(context.Background(), db, "USD", now)
	require.NoError(t, err)
	assert.Equal(t, beforeSummary, afterSummary)
}

func TestGetServersOmitsDeletedClients(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 27, 12, 0)
	expiry := now.AddDate(1, 0, 0)
	live := saveClient(t, db, models.Client{Name: "Legend_SG", Price: 70, BillingCycle: 1095, Currency: "USD", ExpiredAt: &expiry})
	gone := saveClient(t, db, models.Client{Name: "Neburst_HK", Price: 383.04, BillingCycle: 365, Currency: "USD", ExpiredAt: &expiry})
	require.NoError(t, EnsureInitialPriceVersions(db, now))
	require.NoError(t, db.Delete(&models.Client{}, "uuid = ?", gone.UUID).Error)

	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "USD", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, live.UUID, page.Items[0].Client)
	assert.Equal(t, "Legend_SG", page.Items[0].Name)

	remaining, expiring, err := remainingValueSummary(context.Background(), db, "USD", now)
	require.NoError(t, err)
	assert.Greater(t, remaining, int64(0))
	assert.Equal(t, 0, expiring)

	require.NoError(t, CloseOrphanedPriceVersions(db, now))
	var open int64
	require.NoError(t, db.Model(&models.BillingPriceVersion{}).Where("client = ? AND effective_to IS NULL", gone.UUID).Count(&open).Error)
	assert.Zero(t, open)

	overview, err := GetOverview(context.Background(), db, "USD", now)
	require.NoError(t, err)
	assert.NotEqual(t, "0.000000", overview.Summary.Month.Base)
	monthly, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "USD", Now: now, PageSize: 20})
	require.NoError(t, err)
	august := periodByKey(monthly.Items, "2026-08")
	assert.Equal(t, 1, august.ServerCount)
	entries, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "USD", From: "2026-08-01", To: "2026-08-31", Page: 1, PageSize: 100, Now: now,
	})
	require.NoError(t, err)
	for _, row := range entries.Items {
		assert.NotEqual(t, gone.UUID, row.Client)
		assert.NotEqual(t, "Neburst_HK", row.ClientName)
	}
	assert.NotEmpty(t, entries.Items)
}

func TestThirtyDayCycleKeepsListedMonthlyPrice(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 27, 12, 0)
	client := saveClient(t, db, models.Client{Name: "VMRack_LAX_L3_01", Price: 4, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, EnsureInitialPriceVersions(db, beijingTime(2026, time.August, 1, 0, 0)))

	daily, monthlyNative, yearly, err := cycleAverageMicros(models.BillingPriceVersion{PriceMicros: 4_000_000, BillingCycleDays: 30})
	require.NoError(t, err)
	assert.Equal(t, int64(4_000_000/30), daily)
	assert.Equal(t, int64(4_000_000), monthlyNative)
	assert.Equal(t, int64(48_000_000), yearly)

	page, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "USD", Client: client.UUID, From: "2026-08-01", To: "2026-08-31", Page: 1, PageSize: 20, Now: now,
	})
	require.NoError(t, err)
	found := false
	for _, row := range page.Items {
		if row.Type != EntryTypeBaseAccrual {
			continue
		}
		found = true
		assert.Equal(t, "4.000000", row.OriginalAmount)
	}
	assert.True(t, found)

	servers, err := GetServers(context.Background(), db, ServerQuery{Currency: "USD", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, servers.Items, 1)
	require.NotNil(t, servers.Items[0].MonthlyAverage)
	assert.Equal(t, "4.000000", *servers.Items[0].MonthlyAverage)
}

func TestRemainingValueSkipsLongTermExpiry(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 30, 12, 0)
	longTerm := now.AddDate(200, 0, 0)
	placeholder := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	finite := now.Add(30 * 24 * time.Hour)
	saveClient(t, db, models.Client{UUID: "long-term", Name: "long-term", Price: 30, BillingCycle: 30, Currency: "CNY", ExpiredAt: &longTerm})
	saveClient(t, db, models.Client{UUID: "placeholder", Name: "placeholder", Price: 30, BillingCycle: 30, Currency: "CNY", ExpiredAt: &placeholder})
	saveClient(t, db, models.Client{UUID: "finite", Name: "finite", Price: 30, BillingCycle: 30, Currency: "CNY", ExpiredAt: &finite})
	require.NoError(t, EnsureInitialPriceVersions(db, now))

	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	byName := map[string]BillingServerRow{}
	for _, row := range page.Items {
		byName[row.Name] = row
	}

	require.Contains(t, byName, "long-term")
	assert.Nil(t, byName["long-term"].RemainingValue)
	assert.Nil(t, byName["long-term"].RemainingDays)
	require.NotNil(t, byName["long-term"].DailyAverage)
	require.NotNil(t, byName["long-term"].MonthlyAverage)
	require.NotNil(t, byName["long-term"].YearlyAverage)
	assert.Equal(t, "1.000000", *byName["long-term"].DailyAverage)
	assert.Equal(t, "30.000000", *byName["long-term"].MonthlyAverage)
	assert.Equal(t, "360.000000", *byName["long-term"].YearlyAverage)
	assert.Nil(t, byName["placeholder"].RemainingValue)
	require.NotNil(t, byName["finite"].RemainingValue)
	require.NotNil(t, byName["finite"].RemainingDays)

	summary, expiring, err := remainingValueSummary(context.Background(), db, "CNY", now)
	require.NoError(t, err)
	finiteAmount, err := ParseAmountMicros(*byName["finite"].RemainingValue)
	require.NoError(t, err)
	assert.Equal(t, finiteAmount, summary)
	assert.Equal(t, 1, expiring)

	assert.True(t, isLongTermExpiry(&longTerm))
	assert.True(t, isLongTermExpiry(&placeholder))
	dated := time.Date(2027, 5, 14, 0, 0, 0, 0, time.UTC)
	assert.False(t, isLongTermExpiry(&dated))
	assert.False(t, isLongTermExpiry(&finite))

	value, days := remainingValue(models.BillingPriceVersion{
		PriceMicros: 30_000_000, Currency: "CNY", BillingCycleDays: 30, ExpiredAt: &longTerm,
	}, "CNY", nil, now)
	assert.Nil(t, value)
	assert.Nil(t, days)
}

func TestRemainingDaysUsesNearestDayLikeTheme(t *testing.T) {
	now := time.Date(2026, time.September, 4, 9, 20, 0, 0, time.UTC)
	expire := now.Add(251*24*time.Hour + 7*time.Hour)
	_, days := remainingValue(models.BillingPriceVersion{
		PriceMicros: 30_000_000, Currency: "CNY", BillingCycleDays: 30, ExpiredAt: &expire,
	}, "CNY", nil, now)
	require.NotNil(t, days)
	assert.Equal(t, 251, *days)
}

func TestRemainingValueSummaryExcludesAlreadyExpiredServers(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 12, 0)
	expiredAt := now.Add(-time.Minute)
	nearExpiry := now.Add(12 * time.Hour)
	farExpiry := now.AddDate(0, 0, 31)
	saveClient(t, db, models.Client{UUID: "expired", Name: "expired"})
	saveClient(t, db, models.Client{UUID: "near", Name: "near"})
	saveClient(t, db, models.Client{UUID: "far", Name: "far"})
	versions := []models.BillingPriceVersion{
		{Client: "expired", ClientName: "expired", PriceMicros: 30_000_000, Currency: "CNY", CurrencyValid: true, BillingCycleDays: 30, EffectiveFrom: now.AddDate(0, -1, 0), ExpiredAt: &expiredAt, Source: PriceSourceMigration},
		{Client: "near", ClientName: "near", PriceMicros: 30_000_000, Currency: "CNY", CurrencyValid: true, BillingCycleDays: 30, EffectiveFrom: now.AddDate(0, -1, 0), ExpiredAt: &nearExpiry, Source: PriceSourceMigration},
		{Client: "far", ClientName: "far", PriceMicros: 30_000_000, Currency: "CNY", CurrencyValid: true, BillingCycleDays: 30, EffectiveFrom: now.AddDate(0, -1, 0), ExpiredAt: &farExpiry, Source: PriceSourceMigration},
	}
	require.NoError(t, db.Create(&versions).Error)

	_, expiring, err := remainingValueSummary(context.Background(), db, "CNY", now)
	require.NoError(t, err)
	assert.Equal(t, 1, expiring)
}

func TestBillingModelSchemaIsPortable(t *testing.T) {
	for _, model := range []interface{}{models.BillingPriceVersion{}, models.BillingFXSnapshot{}, models.BillingEntry{}} {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		assert.NotEmpty(t, parsed.Table)
		for _, field := range parsed.Fields {
			dataType := strings.ToLower(string(field.DataType))
			assert.NotContains(t, dataType, "sqlite")
			assert.NotContains(t, dataType, "autoincrement")
		}
	}
	db := billingTestDB(t)
	assert.True(t, db.Migrator().HasIndex(&models.BillingEntry{}, "idx_billing_entries_entry_key"))
	assert.True(t, db.Migrator().HasColumn(&models.BillingPriceVersion{}, "price_micros"))
	assert.True(t, db.Migrator().HasColumn(&models.BillingPriceVersion{}, "fx_snapshot_id"))
	assert.True(t, db.Migrator().HasColumn(&models.BillingPriceVersion{}, "usd_price_micros"))
	assert.True(t, db.Migrator().HasColumn(&models.BillingFXSnapshot{}, "rates_json"))
}

func TestGetServersFollowsClientListOrder(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 12, 0)
	created := now.Add(-time.Hour)
	au := saveClient(t, db, models.Client{Name: "AU-01", Weight: 65, CreatedAt: created, Price: 10, BillingCycle: 30, Currency: "USD"})
	ca := saveClient(t, db, models.Client{Name: "CA-01", Weight: 10, CreatedAt: created, Price: 10, BillingCycle: 30, Currency: "USD"})
	br := saveClient(t, db, models.Client{Name: "BR-01", Weight: 40, CreatedAt: created, Price: 10, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, EnsureInitialPriceVersions(db, now))
	page, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)
	assert.Equal(t, []string{ca.UUID, br.UUID, au.UUID}, []string{page.Items[0].Client, page.Items[1].Client, page.Items[2].Client})
}

func TestGetServersFiltersRegionGroupAndSearch(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 12, 0)
	tw := saveClient(t, db, models.Client{Name: "TW-01", Region: "🇹🇼", Group: "亚太", Tags: "bgp;premium", Price: 10, BillingCycle: 30, Currency: "USD"})
	ca := saveClient(t, db, models.Client{Name: "CA-01", Region: "🇨🇦", Group: "北美", Tags: "backup", Price: 10, BillingCycle: 30, Currency: "USD"})
	require.NoError(t, EnsureInitialPriceVersions(db, now))

	assert.Equal(t, "TW", regionKey("🇹🇼"))
	assert.Equal(t, "CA", regionKey("ca"))

	byRegion, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Regions: []string{"TW"}, Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, byRegion.Items, 1)
	assert.Equal(t, tw.UUID, byRegion.Items[0].Client)

	byGroup, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Groups: []string{"亚太"}, Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, byGroup.Items, 1)
	assert.Equal(t, tw.UUID, byGroup.Items[0].Client)

	byTag, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Search: "premium", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, byTag.Items, 1)
	assert.Equal(t, tw.UUID, byTag.Items[0].Client)

	byName, err := GetServers(context.Background(), db, ServerQuery{Currency: "CNY", Search: "ca-01", Now: now, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, byName.Items, 1)
	assert.Equal(t, ca.UUID, byName.Items[0].Client)
}

func TestGetEntriesShowsCommittedBaseInsteadOfDailyAccrual(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 12, 0)
	client := saveClient(t, db, models.Client{Name: "base-node", Price: 30, BillingCycle: 30, Currency: "CNY"})
	require.NoError(t, EnsureInitialPriceVersions(db, beijingTime(2026, time.August, 1, 0, 0)))
	require.NoError(t, EnsureAccruedThrough(context.Background(), db, BeijingDay(now)))
	monthPage, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", From: "2026-08-01", To: "2026-08-31", Page: 1, PageSize: 100, Now: now,
	})
	require.NoError(t, err)
	monthBase := 0
	for _, row := range monthPage.Items {
		if row.Type == EntryTypeBaseAccrual {
			monthBase++
			assert.Equal(t, "base-node", row.ClientName)
			assert.Equal(t, "30.000000", row.OriginalAmount)
			assert.NotContains(t, row.Note, "daily")
		}
	}
	assert.Equal(t, 1, monthBase)

	yearPage, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", From: "2026-01-01", To: "2026-12-31", Page: 1, PageSize: 100, Now: now,
	})
	require.NoError(t, err)
	yearBase := 0
	for _, row := range yearPage.Items {
		if row.Type == EntryTypeBaseAccrual {
			yearBase++
		}
	}
	assert.Equal(t, 1, yearBase)

	serverPage, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", Client: client.UUID, From: "2026-01-01", To: "2026-12-31", Page: 1, PageSize: 100, Now: now,
	})
	require.NoError(t, err)
	serverBase := 0
	for _, row := range serverPage.Items {
		if row.Type == EntryTypeBaseAccrual {
			serverBase++
		}
	}
	assert.GreaterOrEqual(t, serverBase, 1)

	named, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", From: "2026-08-01", To: "2026-08-31", Q: "base-node", Page: 1, PageSize: 100, Now: now,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, named.Items)
	missing, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", From: "2026-08-01", To: "2026-08-31", Q: "missing-server", Page: 1, PageSize: 100, Now: now,
	})
	require.NoError(t, err)
	assert.Empty(t, missing.Items)

	addons, err := GetEntries(context.Background(), db, EntryQuery{
		Currency: "CNY", From: "2026-08-01", To: "2026-08-31", Types: []string{EntryTypeTrafficReset, EntryTypeIPChange}, Page: 1, PageSize: 100, Now: now,
	})
	require.NoError(t, err)
	for _, row := range addons.Items {
		assert.NotEqual(t, EntryTypeBaseAccrual, row.Type)
	}
}
