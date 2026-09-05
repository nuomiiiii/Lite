package billing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

type AmountBreakdown struct {
	Base    string `json:"base"`
	Extra   string `json:"extra"`
	Other   string `json:"other,omitempty"`
	OneTime string `json:"one_time,omitempty"`
	Total   string `json:"total"`
}

type MonthComposition struct {
	AmountBreakdown
	BasePercent    string `json:"base_percent"`
	ExtraPercent   string `json:"extra_percent"`
	OtherPercent   string `json:"other_percent"`
	OneTimePercent string `json:"one_time_percent"`
}

type BillingOverview struct {
	Currency         string           `json:"currency"`
	Timezone         string           `json:"timezone"`
	CoverageStart    *time.Time       `json:"coverage_start"`
	FX               FXState          `json:"fx"`
	Summary          OverviewSummary  `json:"summary"`
	MonthlyTrend     []PeriodAmount   `json:"monthly_trend"`
	PendingFXEntries int64            `json:"pending_fx_entries"`
	MonthComposition MonthComposition `json:"month_composition"`
}

type OverviewSummary struct {
	Today                AmountBreakdown `json:"today"`
	Month                AmountBreakdown `json:"month"`
	Year                 AmountBreakdown `json:"year"`
	RemainingValue       string          `json:"remaining_value"`
	ExpiringWithin30Days int             `json:"expiring_within_30_days"`
}

type PeriodAmount struct {
	Period       string  `json:"period"`
	Base         string  `json:"base"`
	Extra        string  `json:"extra"`
	Other        string  `json:"other"`
	OneTime      string  `json:"one_time"`
	Total        string  `json:"total"`
	Status       string  `json:"status,omitempty"`
	ServerCount  int     `json:"server_count,omitempty"`
	YearOverYear *string `json:"year_over_year,omitempty"`
}

type PageInfo struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
	Pages    int   `json:"pages"`
}

type ServerQuery struct {
	Currency         string
	Search           string
	NativeCurrencies []string
	Regions          []string
	Groups           []string
	ExpiryDays       int
	Page             int
	PageSize         int
	Now              time.Time
}

type BillingServerRow struct {
	Client           string     `json:"client"`
	Name             string     `json:"name"`
	Region           string     `json:"region"`
	Group            string     `json:"group"`
	OriginalAmount   string     `json:"original_amount"`
	OriginalCurrency string     `json:"original_currency"`
	CurrencyValid    bool       `json:"currency_valid"`
	BillingCycleDays int        `json:"billing_cycle_days"`
	BillingStatus    string     `json:"billing_status"`
	DailyAverage     *string    `json:"daily_average"`
	MonthlyAverage   *string    `json:"monthly_average"`
	YearlyAverage    *string    `json:"yearly_average"`
	MonthBase        string     `json:"month_base"`
	MonthExtra       string     `json:"month_extra"`
	MonthTotal       string     `json:"month_total"`
	ExpiredAt        *time.Time `json:"expired_at"`
	RemainingDays    *int       `json:"remaining_days"`
	RemainingValue   *string    `json:"remaining_value"`
}

type ServerPage struct {
	Currency string             `json:"currency"`
	Items    []BillingServerRow `json:"items"`
	Page     PageInfo           `json:"pagination"`
}

type PeriodQuery struct {
	Currency         string
	Years            []int
	Clients          []string
	Types            []string
	NativeCurrencies []string
	Months           []string
	Page             int
	PageSize         int
	Now              time.Time
}

type PeriodPage struct {
	Currency       string          `json:"currency"`
	CoverageStart  *time.Time      `json:"coverage_start"`
	Items          []PeriodAmount  `json:"items"`
	Summary        AmountBreakdown `json:"summary"`
	MonthlyAverage string          `json:"monthly_average,omitempty"`
	YearlyAverage  string          `json:"yearly_average,omitempty"`
	AvailableYears []int           `json:"available_years"`
	Page           PageInfo        `json:"pagination"`
}

type EntryQuery struct {
	Currency string
	Client   string
	From     string
	To       string
	Types    []string
	Q        string
	Page     int
	PageSize int
	Now      time.Time
}

type BillingEntryRow struct {
	ID                uint64    `json:"id"`
	Client            string    `json:"client"`
	ClientName        string    `json:"client_name"`
	Type              string    `json:"type"`
	Category          string    `json:"category"`
	Day               string    `json:"day"`
	OccurredAt        time.Time `json:"occurred_at"`
	OriginalAmount    string    `json:"original_amount"`
	OriginalCurrency  string    `json:"original_currency"`
	ConvertedAmount   *string   `json:"converted_amount"`
	ConvertedCurrency string    `json:"converted_currency"`
	PendingFX         bool      `json:"pending_fx"`
	ReversalOf        *uint64   `json:"reversal_of"`
	Note              string    `json:"note"`
	Operator          string    `json:"operator"`
	Voidable          bool      `json:"voidable"`
	Voided            bool      `json:"voided"`
}

type EntryPage struct {
	Currency string            `json:"currency"`
	Items    []BillingEntryRow `json:"items"`
	Page     PageInfo          `json:"pagination"`
}

type calculatedEntry struct {
	Entry    models.BillingEntry
	Category string
}

type amountAccumulator struct {
	Base, Extra, Other, OneTime, Total int64
}

func GetOverview(ctx context.Context, db *gorm.DB, currency string, now time.Time) (BillingOverview, error) {
	now = normalizedNow(now)
	currency, err := requireDisplayCurrency(currency)
	if err != nil {
		return BillingOverview{}, err
	}
	if err := BackfillPriceVersionFX(db.WithContext(ctx)); err != nil {
		return BillingOverview{}, err
	}
	entries, snapshots, coverage, err := queryCalculatedEntries(ctx, db, now, nil, nil, nil, "", "")
	if err != nil {
		return BillingOverview{}, err
	}
	result := BillingOverview{Currency: currency, Timezone: BeijingLocation.String(), CoverageStart: coverage}
	latest, _, latestErr := LatestFXSnapshot(db.WithContext(ctx))
	if latestErr != nil && latestErr != gorm.ErrRecordNotFound {
		return result, latestErr
	}
	result.FX = FXStatus(latest, now)

	versions, err := listBillableVersions(ctx, db, nil, nil)
	if err != nil {
		return result, err
	}
	meta, err := loadClientBillingMeta(ctx, db, nil)
	if err != nil {
		return result, err
	}

	todayKey := BeijingDay(now).Format(time.DateOnly)
	monthKey := todayKey[:7]
	yearKey := todayKey[:4]
	monthTotals := map[string]*amountAccumulator{}
	localNow := now.In(BeijingLocation)
	yearStart := time.Date(localNow.Year(), 1, 1, 0, 0, 0, 0, BeijingLocation)
	for month := 0; month < 12; month++ {
		key := yearStart.AddDate(0, month, 0).Format("2006-01")
		monthTotals[key] = &amountAccumulator{}
	}
	versionSnapshots, snapErr := loadVersionFXSnapshots(ctx, db, versions)
	if snapErr != nil {
		return result, snapErr
	}
	for id, versionRates := range versionSnapshots {
		snapshots[id] = versionRates
	}
	var today, month, year amountAccumulator
	for _, item := range entries {
		if item.Category == EntryTypeBaseAccrual {
			continue
		}
		amount, ok := convertedEntryAmount(item.Entry, currency, snapshots)
		if !ok {
			result.PendingFXEntries++
			continue
		}
		if item.Entry.Day == todayKey {
			addCategory(&today, item.Category, amount)
		}
		if strings.HasPrefix(item.Entry.Day, monthKey) {
			addCategory(&month, item.Category, amount)
		}
		if strings.HasPrefix(item.Entry.Day, yearKey) {
			addCategory(&year, item.Category, amount)
		}
		if len(item.Entry.Day) >= 7 {
			if accumulator := monthTotals[item.Entry.Day[:7]]; accumulator != nil {
				addCategory(accumulator, item.Category, amount)
			}
		}
	}

	dayStart := BeijingDay(now)
	dayEnd := dayStart.AddDate(0, 0, 1)
	for _, version := range versions {
		if !versionActiveOnDay(version, dayStart, dayEnd) {
			continue
		}
		daily, _, _, calcErr := cycleAverageMicros(version)
		if calcErr != nil {
			return result, calcErr
		}
		amount, ok := convertLockedMicros(daily, version, currency, snapshots)
		if ok {
			addCategory(&today, EntryTypeBaseAccrual, amount)
		}
	}

	from := earliestEffective(versions, now)
	if coverage != nil && coverage.Before(from) {
		from = *coverage
	}
	cycles, err := walkCommittedCycles(versions, meta, currency, snapshots, from, committedHorizon(now, []int{localNow.Year()}, versions))
	if err != nil {
		return result, err
	}
	for _, cycle := range cycles {
		local := cycle.Start.In(BeijingLocation)
		key := local.Format("2006-01")
		if local.Format("2006") == yearKey {
			addCategory(&year, EntryTypeBaseAccrual, cycle.Amount)
		}
		if key == monthKey {
			addCategory(&month, EntryTypeBaseAccrual, cycle.Amount)
		}
		if accumulator := monthTotals[key]; accumulator != nil {
			addCategory(accumulator, EntryTypeBaseAccrual, cycle.Amount)
		}
	}

	result.Summary.Today = formattedBreakdown(today)
	result.Summary.Month = formattedBreakdown(month)
	result.Summary.Year = formattedBreakdown(year)
	result.MonthComposition = compositionFromAccumulator(month)
	keys := make([]string, 0, len(monthTotals))
	for key := range monthTotals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		row := periodFromAccumulator(key, *monthTotals[key])
		result.MonthlyTrend = append(result.MonthlyTrend, row)
	}
	remaining, expiring, err := remainingValueSummary(ctx, db, currency, now)
	if err != nil {
		return result, err
	}
	result.Summary.RemainingValue = FormatAmountMicros(remaining)
	result.Summary.ExpiringWithin30Days = expiring
	return result, nil
}

func GetServers(ctx context.Context, db *gorm.DB, query ServerQuery) (ServerPage, error) {
	query.Now = normalizedNow(query.Now)
	currency, err := requireDisplayCurrency(query.Currency)
	if err != nil {
		return ServerPage{}, err
	}
	if err := BackfillPriceVersionFX(db.WithContext(ctx)); err != nil {
		return ServerPage{}, err
	}
	query.Page, query.PageSize = normalizePage(query.Page, query.PageSize)
	entries, snapshots, _, err := queryCalculatedEntries(ctx, db, query.Now, nil, nil, nil, "", "")
	if err != nil {
		return ServerPage{}, err
	}
	monthKey := BeijingDay(query.Now).Format("2006-01")
	monthly := map[string]*amountAccumulator{}
	for _, item := range entries {
		if item.Category == EntryTypeBaseAccrual {
			continue
		}
		if !strings.HasPrefix(item.Entry.Day, monthKey) {
			continue
		}
		amount, ok := convertedEntryAmount(item.Entry, currency, snapshots)
		if !ok {
			continue
		}
		if monthly[item.Entry.Client] == nil {
			monthly[item.Entry.Client] = &amountAccumulator{}
		}
		addCategory(monthly[item.Entry.Client], item.Category, amount)
	}
	var versions []models.BillingPriceVersion
	if err := db.WithContext(ctx).Where("effective_to IS NULL").Find(&versions).Error; err != nil {
		return ServerPage{}, err
	}
	var clients []models.Client
	if err := db.WithContext(ctx).Select("uuid", "name", "region", "group", "tags", "weight", "created_at").Find(&clients).Error; err != nil {
		return ServerPage{}, err
	}
	clientByID := make(map[string]models.Client, len(clients))
	for _, client := range clients {
		clientByID[client.UUID] = client
	}
	latestSnapshot, latestRates, latestErr := LatestFXSnapshot(db.WithContext(ctx))
	_ = latestSnapshot
	if latestErr != nil && latestErr != gorm.ErrRecordNotFound {
		return ServerPage{}, latestErr
	}
	billable, err := listBillableVersions(ctx, db, nil, nil)
	if err != nil {
		return ServerPage{}, err
	}
	meta, err := loadClientBillingMeta(ctx, db, nil)
	if err != nil {
		return ServerPage{}, err
	}
	from := earliestEffective(billable, query.Now)
	lockedRates, snapErr := loadVersionFXSnapshots(ctx, db, billable)
	if snapErr != nil {
		return ServerPage{}, snapErr
	}
	cycles, err := walkCommittedCycles(billable, meta, currency, lockedRates, from, committedHorizon(query.Now, nil, billable))
	if err != nil {
		return ServerPage{}, err
	}
	committedMonth := map[string]int64{}
	for _, cycle := range cycles {
		if cycle.Start.In(BeijingLocation).Format("2006-01") == monthKey {
			committedMonth[cycle.Client] += cycle.Amount
		}
	}
	nativeSet := stringSet(CanonicalNativeCurrencies(query.NativeCurrencies))
	regionSet := stringSet(query.Regions)
	groupSet := stringSet(query.Groups)
	search := strings.ToLower(strings.TrimSpace(query.Search))
	rows := make([]BillingServerRow, 0, len(versions))
	for _, version := range versions {
		clientName, region, group, tags := version.ClientName, version.Region, version.Group, ""
		if client, ok := clientByID[version.Client]; ok {
			clientName, region, group, tags = client.Name, client.Region, client.Group, client.Tags
		}
		if len(query.NativeCurrencies) > 0 {
			if len(nativeSet) == 0 {
				continue
			}
			canonical, ok := NormalizeCurrency(version.Currency)
			if !ok {
				canonical = version.Currency
			}
			if _, match := nativeSet[canonical]; !match {
				continue
			}
		}
		if len(regionSet) > 0 {
			if _, ok := regionSet[regionKey(region)]; !ok {
				continue
			}
		}
		if len(groupSet) > 0 {
			groupValue := strings.TrimSpace(group)
			if groupValue == "" {
				groupValue = "__none__"
			}
			if _, ok := groupSet[groupValue]; !ok {
				continue
			}
		}
		if search != "" && !serverMatchesSearch(search, clientName, region, group, tags, version.Client) {
			continue
		}
		if _, ok := clientByID[version.Client]; !ok {
			continue
		}
		if query.ExpiryDays > 0 {
			if version.ExpiredAt == nil || version.ExpiredAt.Before(query.Now) || version.ExpiredAt.After(query.Now.AddDate(0, 0, query.ExpiryDays)) {
				continue
			}
		}
		row := BillingServerRow{
			Client: version.Client, Name: clientName, Region: region, Group: group,
			OriginalAmount: FormatAmountMicros(version.PriceMicros), OriginalCurrency: version.Currency,
			CurrencyValid: version.CurrencyValid, BillingCycleDays: version.BillingCycleDays,
			ExpiredAt: version.ExpiredAt,
		}
		switch {
		case version.PriceMicros == -MicrosPerUnit:
			row.BillingStatus = "free"
		case version.PriceMicros <= 0:
			row.BillingStatus = "unconfigured"
		case version.BillingCycleDays == -1:
			row.BillingStatus = "one_time"
		default:
			row.BillingStatus = "recurring"
		}
		extra, other, oneTime := int64(0), int64(0), int64(0)
		if totals := monthly[version.Client]; totals != nil {
			extra, other, oneTime = totals.Extra, totals.Other, totals.OneTime
		}
		base := committedMonth[version.Client]
		row.MonthBase = FormatAmountMicros(base)
		row.MonthExtra = FormatAmountMicros(extra + other + oneTime)
		row.MonthTotal = FormatAmountMicros(base + extra + other + oneTime)
		if row.BillingStatus == "recurring" && version.CurrencyValid {
			dailyNative, monthlyNative, yearlyNative, calcErr := cycleAverageMicros(version)
			if calcErr != nil {
				return ServerPage{}, calcErr
			}
			row.DailyAverage = convertedLockedForecast(dailyNative, version, currency, lockedRates)
			row.MonthlyAverage = convertedLockedForecast(monthlyNative, version, currency, lockedRates)
			row.YearlyAverage = convertedLockedForecast(yearlyNative, version, currency, lockedRates)
			row.RemainingValue, row.RemainingDays = remainingValue(version, currency, latestRates, query.Now)
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, aOk := clientByID[rows[i].Client]
		b, bOk := clientByID[rows[j].Client]
		if aOk && bOk {
			if a.Weight != b.Weight {
				return a.Weight < b.Weight
			}
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			return a.UUID < b.UUID
		}
		if aOk != bOk {
			return aOk
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Client < rows[j].Client
	})
	total := len(rows)
	start, end := pageBounds(total, query.Page, query.PageSize)
	return ServerPage{Currency: currency, Items: rows[start:end], Page: pageInfo(total, query.Page, query.PageSize)}, nil
}

func regionKey(region string) string {
	value := strings.TrimSpace(region)
	if value == "" {
		return "UN"
	}
	if len(value) == 2 && isRegionLetter(value[0]) && isRegionLetter(value[1]) {
		return strings.ToUpper(value)
	}
	runes := []rune(value)
	if len(runes) != 2 {
		return "UN"
	}
	const start, end = 0x1F1E6, 0x1F1FF
	if runes[0] < start || runes[0] > end || runes[1] < start || runes[1] > end {
		return "UN"
	}
	return string([]byte{byte('A' + (runes[0] - start)), byte('A' + (runes[1] - start))})
}

func isRegionLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func serverMatchesSearch(needle, name, region, group, tags, client string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		name, region, regionKey(region), group, strings.ReplaceAll(tags, ";", " "), client,
	}, " "))
	return strings.Contains(haystack, needle)
}

func GetMonthly(ctx context.Context, db *gorm.DB, query PeriodQuery) (PeriodPage, error) {
	query.Now = normalizedNow(query.Now)
	if err := normalizeMonthlyMonths(&query); err != nil {
		return PeriodPage{}, err
	}
	return getPeriods(ctx, db, query, true)
}

func GetYearly(ctx context.Context, db *gorm.DB, query PeriodQuery) (PeriodPage, error) {
	query.Now = normalizedNow(query.Now)
	if len(query.Years) == 0 {
		query.Years = []int{BeijingDay(query.Now).Year()}
	}
	return getPeriods(ctx, db, query, false)
}

func normalizeMonthlyMonths(query *PeriodQuery) error {
	if len(query.Months) == 0 {
		query.Months = []string{BeijingDay(query.Now).Format("2006-01")}
	}
	normalized := make([]string, 0, len(query.Months))
	seen := map[string]struct{}{}
	years := make([]int, 0, len(query.Months))
	yearSeen := map[int]struct{}{}
	for _, month := range query.Months {
		parsed, err := time.ParseInLocation("2006-01", month, BeijingLocation)
		if err != nil {
			return invalidInputf("months contains an invalid month")
		}
		key := parsed.Format("2006-01")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
		year := parsed.Year()
		if _, ok := yearSeen[year]; !ok {
			yearSeen[year] = struct{}{}
			years = append(years, year)
		}
	}
	query.Months = normalized
	query.Years = years
	return nil
}

func getPeriods(ctx context.Context, db *gorm.DB, query PeriodQuery, monthly bool) (PeriodPage, error) {
	currency, err := requireDisplayCurrency(query.Currency)
	if err != nil {
		return PeriodPage{}, err
	}
	if err := BackfillPriceVersionFX(db.WithContext(ctx)); err != nil {
		return PeriodPage{}, err
	}
	query.Page, query.PageSize = normalizePage(query.Page, query.PageSize)
	entries, snapshots, coverage, err := queryCalculatedEntries(ctx, db, query.Now, query.Clients, query.Types, query.NativeCurrencies, "", "")
	if err != nil {
		return PeriodPage{}, err
	}
	yearSet := intSet(query.Years)
	monthSet := stringSet(query.Months)
	availableSet := map[int]struct{}{}
	periods := map[string]*amountAccumulator{}
	servers := map[string]map[string]struct{}{}
	typeSet := stringSet(query.Types)
	_, includeBase := typeSet[EntryTypeBaseAccrual]
	if len(typeSet) == 0 {
		includeBase = true
	}
	for _, item := range entries {
		if item.Category == EntryTypeBaseAccrual {
			continue
		}
		if len(item.Entry.Day) < 4 {
			continue
		}
		year, _ := strconv.Atoi(item.Entry.Day[:4])
		availableSet[year] = struct{}{}
		if len(yearSet) > 0 {
			if _, ok := yearSet[year]; !ok {
				continue
			}
		}
		period := item.Entry.Day[:4]
		if monthly && len(item.Entry.Day) >= 7 {
			period = item.Entry.Day[:7]
		}
		if monthly {
			if _, ok := monthSet[period]; !ok {
				continue
			}
		}
		if periods[period] == nil {
			periods[period] = &amountAccumulator{}
			servers[period] = map[string]struct{}{}
		}
		amount, ok := convertedEntryAmount(item.Entry, currency, snapshots)
		if !ok {
			continue
		}
		addCategory(periods[period], item.Category, amount)
		servers[period][item.Entry.Client] = struct{}{}
	}
	if includeBase {
		versions, listErr := listBillableVersions(ctx, db, query.Clients, query.NativeCurrencies)
		if listErr != nil {
			return PeriodPage{}, listErr
		}
		meta, metaErr := loadClientBillingMeta(ctx, db, query.Clients)
		if metaErr != nil {
			return PeriodPage{}, metaErr
		}
		from := earliestEffective(versions, query.Now)
		if coverage != nil && coverage.Before(from) {
			from = *coverage
		}
		lockedRates, snapErr := loadVersionFXSnapshots(ctx, db, versions)
		if snapErr != nil {
			return PeriodPage{}, snapErr
		}
		cycles, walkErr := walkCommittedCycles(versions, meta, currency, lockedRates, from, committedHorizon(query.Now, query.Years, versions))
		if walkErr != nil {
			return PeriodPage{}, walkErr
		}
		addCommittedCycles(periods, servers, availableSet, cycles, yearSet, monthSet, monthly)
	}
	if monthly {
		for _, key := range query.Months {
			if periods[key] == nil {
				periods[key] = &amountAccumulator{}
				servers[key] = map[string]struct{}{}
			}
		}
	}
	keys := make([]string, 0, len(periods))
	for key := range periods {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	rows := make([]PeriodAmount, 0, len(keys))
	var summary amountAccumulator
	completePeriods := 0
	var completePeriodTotal int64
	for _, key := range keys {
		row := periodFromAccumulator(key, *periods[key])
		row.ServerCount = len(servers[key])
		empty := periods[key].Total == 0 && len(servers[key]) == 0
		switch {
		case empty && coverage != nil && periodEndsBeforeCoverage(key, monthly, *coverage):
			row.Status = "no_record"
		case empty:
			row.Status = "no_record"
		case periodIsFuture(key, monthly, query.Now):
			row.Status = "projected"
		case periodIsCurrent(key, monthly, query.Now):
			row.Status = "in_progress"
		default:
			row.Status = "settled"
		}
		completePeriods++
		completePeriodTotal += periods[key].Total
		if row.Status != "no_record" {
			addAccumulator(&summary, *periods[key])
		}
		rows = append(rows, row)
	}
	if !monthly {
		for index := range rows {
			current, _ := ParseAmountMicros(rows[index].Total)
			for _, previous := range rows {
				currentYear, _ := strconv.Atoi(rows[index].Period)
				previousYear, _ := strconv.Atoi(previous.Period)
				if previousYear != currentYear-1 {
					continue
				}
				prev, _ := ParseAmountMicros(previous.Total)
				if prev != 0 {
					change := fmt.Sprintf("%.1f", (float64(current-prev)/float64(prev))*100)
					rows[index].YearOverYear = &change
				}
			}
		}
	}
	available := make([]int, 0, len(availableSet))
	for year := range availableSet {
		available = append(available, year)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(available)))
	total := len(rows)
	start, end := pageBounds(total, query.Page, query.PageSize)
	result := PeriodPage{Currency: currency, CoverageStart: coverage, Items: rows[start:end], Summary: formattedBreakdown(summary), AvailableYears: available, Page: pageInfo(total, query.Page, query.PageSize)}
	if completePeriods > 0 {
		average := FormatAmountMicros(completePeriodTotal / int64(completePeriods))
		if monthly {
			result.MonthlyAverage = average
		} else {
			result.YearlyAverage = average
		}
	}
	return result, nil
}

func GetEntries(ctx context.Context, db *gorm.DB, query EntryQuery) (EntryPage, error) {
	query.Now = normalizedNow(query.Now)
	currency, err := requireDisplayCurrency(query.Currency)
	if err != nil {
		return EntryPage{}, err
	}
	query.Page, query.PageSize = normalizePage(query.Page, query.PageSize)
	clients := []string{}
	if query.Client != "" {
		clients = []string{query.Client}
	}
	entries, snapshots, coverage, err := queryCalculatedEntries(ctx, db, query.Now, clients, query.Types, nil, query.From, query.To)
	if err != nil {
		return EntryPage{}, err
	}
	voidedIDs, err := reversedOriginalIDs(ctx, db, clients)
	if err != nil {
		return EntryPage{}, err
	}
	rows := make([]BillingEntryRow, 0, len(entries))
	for _, item := range entries {
		if item.Entry.Type == EntryTypeBaseAccrual {
			continue
		}
		amount, ok := convertedEntryAmount(item.Entry, currency, snapshots)
		var converted *string
		if ok {
			value := FormatAmountMicros(amount)
			converted = &value
		}
		_, voided := voidedIDs[item.Entry.ID]
		rows = append(rows, BillingEntryRow{
			ID: item.Entry.ID, Client: item.Entry.Client, ClientName: item.Entry.ClientName,
			Type: item.Entry.Type, Category: item.Category, Day: item.Entry.Day, OccurredAt: item.Entry.OccurredAt,
			OriginalAmount: FormatAmountMicros(item.Entry.OriginalAmountMicros), OriginalCurrency: item.Entry.OriginalCurrency,
			ConvertedAmount: converted, ConvertedCurrency: currency, PendingFX: !ok, ReversalOf: item.Entry.ReversalOf,
			Note: item.Entry.Note, Operator: item.Entry.Operator,
			Voidable: item.Entry.ID > 0 && item.Entry.Type != EntryTypeReversal && !voided,
			Voided:   voided,
		})
	}
	typeSet := stringSet(query.Types)
	includeBase := len(typeSet) == 0
	if _, ok := typeSet[EntryTypeBaseAccrual]; ok {
		includeBase = true
	}
	if includeBase {
		baseRows, baseErr := committedBaseEntryRows(ctx, db, query, currency, coverage)
		if baseErr != nil {
			return EntryPage{}, baseErr
		}
		rows = append(rows, baseRows...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].OccurredAt.Equal(rows[j].OccurredAt) {
			if rows[i].Client == rows[j].Client {
				return rows[i].ID > rows[j].ID
			}
			return rows[i].Client < rows[j].Client
		}
		return rows[i].OccurredAt.After(rows[j].OccurredAt)
	})
	if needle := strings.ToLower(strings.TrimSpace(query.Q)); needle != "" {
		filtered := make([]BillingEntryRow, 0, len(rows))
		for _, row := range rows {
			if strings.Contains(strings.ToLower(row.ClientName), needle) || strings.Contains(strings.ToLower(row.Client), needle) {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	total := len(rows)
	start, end := pageBounds(total, query.Page, query.PageSize)
	return EntryPage{Currency: currency, Items: rows[start:end], Page: pageInfo(total, query.Page, query.PageSize)}, nil
}

type committedBaseBucket struct {
	Client           string
	ClientName       string
	OriginalCurrency string
	Day              string
	OccurredAt       time.Time
	Native           int64
	Converted        int64
}

func entryBaseGrain(from, to, client string) string {
	if client != "" {
		return "month"
	}
	if len(from) >= 7 && len(to) >= 7 && from[:7] == to[:7] {
		return "month"
	}
	return "year"
}

func yearsFromEntryRange(from, to string, now time.Time) []int {
	seen := map[int]struct{}{}
	add := func(value string) {
		if len(value) < 4 {
			return
		}
		year, err := strconv.Atoi(value[:4])
		if err != nil {
			return
		}
		seen[year] = struct{}{}
	}
	add(from)
	add(to)
	if len(seen) == 0 {
		return []int{BeijingDay(now).Year()}
	}
	years := make([]int, 0, len(seen))
	for year := range seen {
		years = append(years, year)
	}
	sort.Ints(years)
	return years
}

func committedBaseEntryRows(ctx context.Context, db *gorm.DB, query EntryQuery, currency string, coverage *time.Time) ([]BillingEntryRow, error) {
	clients := []string{}
	if query.Client != "" {
		clients = []string{query.Client}
	}
	versions, err := listBillableVersions(ctx, db, clients, nil)
	if err != nil {
		return nil, err
	}
	meta, err := loadClientBillingMeta(ctx, db, clients)
	if err != nil {
		return nil, err
	}
	from := earliestEffective(versions, query.Now)
	if coverage != nil && coverage.Before(from) {
		from = *coverage
	}
	lockedRates, err := loadVersionFXSnapshots(ctx, db, versions)
	if err != nil {
		return nil, err
	}
	years := yearsFromEntryRange(query.From, query.To, query.Now)
	cycles, err := walkCommittedCycles(versions, meta, currency, lockedRates, from, committedHorizon(query.Now, years, versions))
	if err != nil {
		return nil, err
	}
	grain := entryBaseGrain(query.From, query.To, query.Client)
	buckets := map[string]*committedBaseBucket{}
	for _, cycle := range cycles {
		local := BeijingDay(cycle.Start)
		day := local.Format(time.DateOnly)
		if query.From != "" && day < query.From {
			continue
		}
		if query.To != "" && day > query.To {
			continue
		}
		period := local.Format("2006")
		bucketDay := local.Format("2006") + "-01-01"
		if grain == "month" {
			period = local.Format("2006-01")
			bucketDay = local.Format("2006-01") + "-01"
		}
		key := cycle.Client + "|" + period
		bucket := buckets[key]
		if bucket == nil {
			bucket = &committedBaseBucket{
				Client: cycle.Client, ClientName: cycle.Version.ClientName,
				OriginalCurrency: cycle.Version.Currency, Day: bucketDay, OccurredAt: local,
			}
			buckets[key] = bucket
		}
		bucket.Native += cycle.NativeAmount
		bucket.Converted += cycle.Amount
		if local.After(bucket.OccurredAt) {
			bucket.OccurredAt = local
			bucket.ClientName = cycle.Version.ClientName
			bucket.OriginalCurrency = cycle.Version.Currency
		}
	}
	rows := make([]BillingEntryRow, 0, len(buckets))
	for _, bucket := range buckets {
		converted := FormatAmountMicros(bucket.Converted)
		rows = append(rows, BillingEntryRow{
			Client: bucket.Client, ClientName: bucket.ClientName,
			Type: EntryTypeBaseAccrual, Category: EntryTypeBaseAccrual,
			Day: bucket.Day, OccurredAt: bucket.OccurredAt,
			OriginalAmount: FormatAmountMicros(bucket.Native), OriginalCurrency: bucket.OriginalCurrency,
			ConvertedAmount: &converted, ConvertedCurrency: currency,
		})
	}
	return rows, nil
}

func queryCalculatedEntries(ctx context.Context, db *gorm.DB, now time.Time, clients, types, nativeCurrencies []string, from, to string) ([]calculatedEntry, map[uint64]map[string]string, *time.Time, error) {
	if err := EnsureAccruedThrough(ctx, db, yesterdayInBeijing(now)); err != nil {
		return nil, nil, nil, err
	}
	query := db.WithContext(ctx).Order("occurred_at ASC, id ASC").
		Where(existingClientCondition("billing_entries.client"))
	if len(clients) > 0 {
		query = query.Where("client IN ?", clients)
	}
	if len(nativeCurrencies) > 0 {
		canonical := CanonicalNativeCurrencies(nativeCurrencies)
		if len(canonical) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("original_currency IN ?", canonical)
		}
	}
	if from != "" {
		query = query.Where("day >= ?", from)
	}
	if to != "" {
		query = query.Where("day <= ?", to)
	}
	var stored []models.BillingEntry
	if err := query.Find(&stored).Error; err != nil {
		return nil, nil, nil, err
	}
	current, err := CurrentDayAccruals(ctx, db, now)
	if err != nil {
		return nil, nil, nil, err
	}
	clientSet := stringSet(clients)
	nativeSet := stringSet(CanonicalNativeCurrencies(nativeCurrencies))
	for _, entry := range current {
		if len(clientSet) > 0 {
			if _, ok := clientSet[entry.Client]; !ok {
				continue
			}
		}
		if len(nativeCurrencies) > 0 {
			if len(nativeSet) == 0 {
				continue
			}
			canonical, ok := NormalizeCurrency(entry.OriginalCurrency)
			if !ok {
				canonical = entry.OriginalCurrency
			}
			if _, match := nativeSet[canonical]; !match {
				continue
			}
		}
		if from != "" && entry.Day < from {
			continue
		}
		if to != "" && entry.Day > to {
			continue
		}
		stored = append(stored, entry)
	}
	originalTypes := map[uint64]string{}
	for _, entry := range stored {
		if entry.ID > 0 {
			originalTypes[entry.ID] = entry.Type
		}
	}
	typeSet := stringSet(types)
	voidedIDs, err := reversedOriginalIDs(ctx, db, clients)
	if err != nil {
		return nil, nil, nil, err
	}
	calculated := make([]calculatedEntry, 0, len(stored))
	snapshotIDs := map[uint64]struct{}{}
	for _, entry := range stored {
		category := entry.Type
		if entry.Type == EntryTypeReversal && entry.ReversalOf != nil {
			if original := originalTypes[*entry.ReversalOf]; original != "" {
				category = original
			}
		}
		if !entryMatchesTypes(entry, category, typeSet, voidedIDs) {
			continue
		}
		if entry.FXSnapshotID != nil {
			snapshotIDs[*entry.FXSnapshotID] = struct{}{}
		}
		calculated = append(calculated, calculatedEntry{Entry: entry, Category: category})
	}
	snapshots := map[uint64]map[string]string{}
	if len(snapshotIDs) > 0 {
		ids := make([]uint64, 0, len(snapshotIDs))
		for id := range snapshotIDs {
			ids = append(ids, id)
		}
		var rows []models.BillingFXSnapshot
		if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, nil, nil, err
		}
		for _, row := range rows {
			rates, parseErr := ParseRatesJSON(row.RatesJSON)
			if parseErr == nil {
				snapshots[row.ID] = rates
			}
		}
	}
	coverage, err := coverageStart(ctx, db)
	return calculated, snapshots, coverage, err
}

func reversedOriginalIDs(ctx context.Context, db *gorm.DB, clients []string) (map[uint64]struct{}, error) {
	query := db.WithContext(ctx).Model(&models.BillingEntry{}).Where("type = ? AND reversal_of IS NOT NULL", EntryTypeReversal)
	if len(clients) > 0 {
		query = query.Where("client IN ?", clients)
	}
	var ids []uint64
	if err := query.Pluck("reversal_of", &ids).Error; err != nil {
		return nil, err
	}
	out := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}

func entryMatchesTypes(entry models.BillingEntry, category string, typeSet map[string]struct{}, voidedIDs map[uint64]struct{}) bool {
	if len(typeSet) == 0 {
		return true
	}
	if _, ok := typeSet[category]; ok {
		return true
	}
	if _, ok := typeSet[entry.Type]; ok {
		return true
	}
	if _, want := typeSet["voided"]; want {
		if _, voided := voidedIDs[entry.ID]; voided {
			return true
		}
	}
	return false
}

func coverageStart(ctx context.Context, db *gorm.DB) (*time.Time, error) {
	var version models.BillingPriceVersion
	if err := db.WithContext(ctx).
		Where(existingClientCondition("billing_price_versions.client")).
		Order("effective_from ASC, id ASC").First(&version).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	value := version.EffectiveFrom.In(BeijingLocation)
	return &value, nil
}

func convertedEntryAmount(entry models.BillingEntry, currency string, snapshots map[uint64]map[string]string) (int64, bool) {
	if entry.OriginalCurrency == currency {
		return entry.OriginalAmountMicros, true
	}
	if entry.FXSnapshotID == nil {
		return 0, false
	}
	rates := snapshots[*entry.FXSnapshotID]
	if len(rates) == 0 {
		return 0, false
	}
	value, err := ConvertMicros(entry.OriginalAmountMicros, entry.OriginalCurrency, currency, rates)
	return value, err == nil
}

func addCategory(total *amountAccumulator, category string, amount int64) {
	switch category {
	case EntryTypeBaseAccrual, EntryTypeOneTime:
		total.Base += amount
	case EntryTypeTrafficReset:
		total.Extra += amount
	case EntryTypeIPChange:
		total.Other += amount
	case EntryTypeAdjustment:
		total.OneTime += amount
	}
	total.Total += amount
}

func addAccumulator(target *amountAccumulator, source amountAccumulator) {
	target.Base += source.Base
	target.Extra += source.Extra
	target.Other += source.Other
	target.OneTime += source.OneTime
	target.Total += source.Total
}

func formattedBreakdown(value amountAccumulator) AmountBreakdown {
	return AmountBreakdown{
		Base:    FormatAmountMicros(value.Base),
		Extra:   FormatAmountMicros(value.Extra),
		Other:   FormatAmountMicros(value.Other),
		OneTime: FormatAmountMicros(value.OneTime),
		Total:   FormatAmountMicros(value.Total),
	}
}

func compositionFromAccumulator(value amountAccumulator) MonthComposition {
	result := MonthComposition{AmountBreakdown: formattedBreakdown(value)}
	if value.Total == 0 {
		return result
	}
	result.BasePercent = percentString(value.Base, value.Total)
	result.ExtraPercent = percentString(value.Extra, value.Total)
	result.OtherPercent = percentString(value.Other, value.Total)
	result.OneTimePercent = percentString(value.OneTime, value.Total)
	return result
}

func percentString(part, total int64) string {
	return strconv.FormatFloat(float64(part)*100/float64(total), 'f', 2, 64)
}

func periodFromAccumulator(period string, value amountAccumulator) PeriodAmount {
	return PeriodAmount{
		Period:  period,
		Base:    FormatAmountMicros(value.Base),
		Extra:   FormatAmountMicros(value.Extra),
		Other:   FormatAmountMicros(value.Other),
		OneTime: FormatAmountMicros(value.OneTime),
		Total:   FormatAmountMicros(value.Total),
	}
}

func requireDisplayCurrency(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "CNY"
	}
	value, valid := NormalizeCurrency(value)
	if !valid || (value != "CNY" && value != "USD") {
		return "", invalidInputf("currency must be CNY or USD")
	}
	return value, nil
}

func normalizedNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func normalizePage(page, size int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func pageBounds(total, page, size int) (int, int) {
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return start, end
}

func pageInfo(total, page, size int) PageInfo {
	pages := 0
	if total > 0 {
		pages = (total + size - 1) / size
	}
	return PageInfo{Page: page, PageSize: size, Total: int64(total), Pages: pages}
}

func stringSet(values []string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func intSet(values []int) map[int]struct{} {
	result := map[int]struct{}{}
	for _, value := range values {
		if value > 0 {
			result[value] = struct{}{}
		}
	}
	return result
}

func convertedForecast(amount int64, from, to string, rates map[string]string) *string {
	if from == to {
		value := FormatAmountMicros(amount)
		return &value
	}
	converted, err := ConvertMicros(amount, from, to, rates)
	if err != nil {
		return nil
	}
	value := FormatAmountMicros(converted)
	return &value
}

func convertedLockedForecast(amount int64, version models.BillingPriceVersion, to string, snapshots map[uint64]map[string]string) *string {
	converted, ok := convertLockedMicros(amount, version, to, snapshots)
	if !ok {
		return nil
	}
	value := FormatAmountMicros(converted)
	return &value
}

func isLongTermExpiry(expiredAt *time.Time) bool {
	if expiredAt == nil {
		return true
	}
	stamp := expiredAt.UTC()
	return stamp.IsZero() || stamp.Year() < 2 || stamp.Year() > 2200
}

func remainingValue(version models.BillingPriceVersion, currency string, rates map[string]string, now time.Time) (*string, *int) {
	if version.ExpiredAt == nil || version.PriceMicros <= 0 || version.BillingCycleDays <= 0 {
		return nil, nil
	}
	if isLongTermExpiry(version.ExpiredAt) {
		return nil, nil
	}
	remaining := version.ExpiredAt.Sub(now)
	days := int(math.Round(remaining.Hours() / 24))
	if remaining <= 0 {
		days = 0
		value := "0.000000"
		return &value, &days
	}
	cycle := time.Duration(version.BillingCycleDays) * 24 * time.Hour
	amount, err := multiplyRatio(version.PriceMicros, remaining.Nanoseconds(), cycle.Nanoseconds())
	if err != nil {
		return nil, &days
	}
	return convertedForecast(amount, version.Currency, currency, rates), &days
}

func remainingValueSummary(ctx context.Context, db *gorm.DB, currency string, now time.Time) (int64, int, error) {
	_, rates, err := LatestFXSnapshot(db.WithContext(ctx))
	if err != nil && err != gorm.ErrRecordNotFound {
		return 0, 0, err
	}
	var versions []models.BillingPriceVersion
	if err := db.WithContext(ctx).Where("effective_to IS NULL").Find(&versions).Error; err != nil {
		return 0, 0, err
	}
	var live []string
	if err := db.WithContext(ctx).Model(&models.Client{}).Pluck("uuid", &live).Error; err != nil {
		return 0, 0, err
	}
	liveSet := stringSet(live)
	var total int64
	expiring := 0
	for _, version := range versions {
		if _, ok := liveSet[version.Client]; !ok {
			continue
		}
		value, days := remainingValue(version, currency, rates, now)
		if value != nil {
			amount, parseErr := ParseAmountMicros(*value)
			if parseErr == nil {
				total += amount
			}
		}
		if days != nil && version.ExpiredAt != nil && version.ExpiredAt.After(now) && !version.ExpiredAt.After(now.AddDate(0, 0, 30)) {
			expiring++
		}
	}
	return total, expiring, nil
}

func periodEndsBeforeCoverage(key string, monthly bool, coverage time.Time) bool {
	if monthly {
		start, err := time.ParseInLocation("2006-01", key, BeijingLocation)
		if err != nil {
			return false
		}
		return !start.AddDate(0, 1, 0).After(coverage)
	}
	start, err := time.ParseInLocation("2006", key, BeijingLocation)
	if err != nil {
		return false
	}
	return !start.AddDate(1, 0, 0).After(coverage)
}

func periodIsCurrent(key string, monthly bool, now time.Time) bool {
	layout := "2006"
	if monthly {
		layout = "2006-01"
	}
	return key == BeijingDay(now).Format(layout)
}

func periodIsFuture(key string, monthly bool, now time.Time) bool {
	layout := "2006"
	if monthly {
		layout = "2006-01"
	}
	return key > BeijingDay(now).Format(layout)
}
