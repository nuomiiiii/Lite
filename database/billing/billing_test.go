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

func saveFX(t *testing.T, db *gorm.DB, fetchedAt time.Time, cny, cad string) models.BillingFXSnapshot {
	t.Helper()
	snapshot := models.BillingFXSnapshot{
		Provider: FXProvider, BaseCurrency: "USD",
		RatesJSON: fmt.Sprintf(`{"CAD":"%s","CNY":"%s","USD":"1"}`, cad, cny),
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
	for _, value := range []string{"XYZ", "ABC", "12A"} {
		_, valid := NormalizeCurrency(value)
		assert.False(t, valid, value)
	}
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

	early, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Now: beijingTime(2026, time.August, 16, 12, 0), PageSize: 20})
	require.NoError(t, err)
	late, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Now: beijingTime(2026, time.August, 26, 12, 0), PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, expected, periodByKey(early.Items, "2026-08").Base)
	assert.Equal(t, periodByKey(early.Items, "2026-08").Base, periodByKey(late.Items, "2026-08").Base)
	assert.Equal(t, "projected", periodByKey(late.Items, "2026-09").Status)
	assert.Equal(t, expected, periodByKey(late.Items, "2026-09").Base)

	nextYear, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Years: []int{2027}, Now: beijingTime(2026, time.August, 26, 12, 0), PageSize: 20})
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

func TestMonthlyDefaultsCurrentYearAndMultiYearSortsAndPages(t *testing.T) {
	db := billingTestDB(t)
	now := beijingTime(2026, time.August, 25, 12, 0)
	client := saveClient(t, db, models.Client{Name: "periods", Currency: "CNY"})
	for index, day := range []string{"2024-12-31", "2025-12-31", "2026-01-02"} {
		entry := models.BillingEntry{EntryKey: fmt.Sprintf("period-%d", index), Client: client.UUID, ClientName: client.Name, Type: EntryTypeTrafficReset, Day: day, OccurredAt: now.Add(time.Duration(index) * time.Minute), OriginalAmountMicros: 1_000_000, OriginalCurrency: "CNY"}
		require.NoError(t, db.Create(&entry).Error)
	}
	defaultPage, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Now: now, PageSize: 20})
	require.NoError(t, err)
	for _, row := range defaultPage.Items {
		assert.True(t, strings.HasPrefix(row.Period, "2026-"))
	}
	assert.Equal(t, "2026-12", defaultPage.Items[0].Period)

	multi, err := GetMonthly(context.Background(), db, PeriodQuery{Currency: "CNY", Years: []int{2025, 2026}, Now: now, Page: 1, PageSize: 2})
	require.NoError(t, err)
	require.Len(t, multi.Items, 2)
	assert.Equal(t, "2026-12", multi.Items[0].Period)
	assert.Equal(t, "2026-11", multi.Items[1].Period)
	assert.Greater(t, multi.Page.Total, int64(2))
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
