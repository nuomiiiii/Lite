package billing

import (
	"context"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"gorm.io/gorm"
)

type clientBillingMeta struct {
	ResetDay int
}

type committedCycle struct {
	Start        time.Time
	End          time.Time
	Client       string
	Version      models.BillingPriceVersion
	Amount       int64
	NativeAmount int64
}

func existingClientCondition(clientColumn string) string {
	return "EXISTS (SELECT 1 FROM clients WHERE clients.uuid = " + clientColumn + ")"
}

func listBillableVersions(ctx context.Context, db *gorm.DB, clients, nativeCurrencies []string) ([]models.BillingPriceVersion, error) {
	query := db.WithContext(ctx).
		Where("price_micros > 0 AND billing_cycle_days > 0 AND currency_valid = ?", true).
		Where(existingClientCondition("billing_price_versions.client"))
	if len(clients) > 0 {
		query = query.Where("client IN ?", clients)
	}
	if len(nativeCurrencies) > 0 {
		canonical := CanonicalNativeCurrencies(nativeCurrencies)
		if len(canonical) == 0 {
			query = query.Where("1 = 0")
		} else {
			query = query.Where("currency IN ?", canonical)
		}
	}
	var versions []models.BillingPriceVersion
	if err := query.Order("effective_from ASC, id ASC").Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

func loadClientBillingMeta(ctx context.Context, db *gorm.DB, clients []string) (map[string]clientBillingMeta, error) {
	query := db.WithContext(ctx).Model(&models.Client{}).Select("uuid", "traffic_reset_day")
	if len(clients) > 0 {
		query = query.Where("uuid IN ?", clients)
	}
	var rows []models.Client
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]clientBillingMeta, len(rows))
	for _, row := range rows {
		result[row.UUID] = clientBillingMeta{ResetDay: trafficledger.NormalizedResetDay(row.TrafficResetDay)}
	}
	return result, nil
}

func billingCycleMonths(days int) int {
	switch days {
	case 30:
		return 1
	case 90, 91, 92:
		return 3
	case 180, 181, 182, 183, 184:
		return 6
	case 365, 366:
		return 12
	case 730, 731:
		return 24
	case 1095, 1096:
		return 36
	default:
		if days <= 0 {
			return 1
		}
		months := (days + 15) / 30
		if months < 1 {
			months = 1
		}
		return months
	}
}

func cycleAverageMicros(version models.BillingPriceVersion) (daily, monthly, yearly int64, err error) {
	if version.BillingCycleDays <= 0 || version.PriceMicros <= 0 {
		return 0, 0, 0, nil
	}
	daily, err = multiplyRatio(version.PriceMicros, 1, int64(version.BillingCycleDays))
	if err != nil {
		return 0, 0, 0, err
	}
	months := int64(billingCycleMonths(version.BillingCycleDays))
	monthly, err = multiplyRatio(version.PriceMicros, 1, months)
	if err != nil {
		return 0, 0, 0, err
	}
	yearly, err = multiplyRatio(version.PriceMicros, 12, months)
	return daily, monthly, yearly, err
}

func convertLockedMicros(nativeAmount int64, version models.BillingPriceVersion, to string, snapshots map[uint64]map[string]string) (int64, bool) {
	if version.Currency == to {
		return nativeAmount, true
	}
	if nativeAmount == 0 {
		return 0, true
	}
	if version.USDPriceMicros == nil || version.FXSnapshotID == nil || version.PriceMicros == 0 {
		return 0, false
	}
	rates := snapshots[*version.FXSnapshotID]
	if len(rates) == 0 {
		return 0, false
	}
	usd, err := multiplyRatio(*version.USDPriceMicros, nativeAmount, version.PriceMicros)
	if err != nil {
		return 0, false
	}
	converted, err := ConvertMicros(usd, "USD", to, rates)
	return converted, err == nil
}

func paidUntil(version models.BillingPriceVersion) *time.Time {
	var end *time.Time
	if version.EffectiveTo != nil {
		end = version.EffectiveTo
	}
	if version.ExpiredAt != nil && (end == nil || version.ExpiredAt.Before(*end)) {
		end = version.ExpiredAt
	}
	return end
}

func versionOverlapsCycle(version models.BillingPriceVersion, start, end time.Time) bool {
	if version.PriceMicros <= 0 || version.BillingCycleDays <= 0 || !version.CurrencyValid {
		return false
	}
	if !version.EffectiveFrom.Before(end) {
		return false
	}
	if version.EffectiveTo != nil && !version.EffectiveTo.After(start) {
		return false
	}
	if version.ExpiredAt != nil && !version.ExpiredAt.After(start) {
		return false
	}
	return true
}

func latestVersionForCycle(versions []models.BillingPriceVersion, client string, start, end time.Time) *models.BillingPriceVersion {
	var best *models.BillingPriceVersion
	for index := range versions {
		version := &versions[index]
		if version.Client != client || !versionOverlapsCycle(*version, start, end) {
			continue
		}
		if best == nil || version.EffectiveFrom.After(best.EffectiveFrom) || (version.EffectiveFrom.Equal(best.EffectiveFrom) && version.ID > best.ID) {
			best = version
		}
	}
	return best
}

func iterCycleStarts(resetDay int, from, until time.Time) []time.Time {
	if until.IsZero() || !from.Before(until) {
		return nil
	}
	start := trafficledger.CycleContaining(resetDay, from)
	starts := make([]time.Time, 0, 24)
	for i := 0; i < 240 && start.Before(until); i++ {
		starts = append(starts, start)
		start = trafficledger.NextCycleStart(start, resetDay)
	}
	return starts
}

func displayHorizon(now time.Time, years []int) time.Time {
	local := BeijingDay(now)
	year := local.Year()
	for _, value := range years {
		if value > year {
			year = value
		}
	}
	return time.Date(year+1, 1, 1, 0, 0, 0, 0, BeijingLocation)
}

func committedHorizon(now time.Time, years []int, versions []models.BillingPriceVersion) time.Time {
	horizon := displayHorizon(now, years)
	for _, version := range versions {
		if paid := paidUntil(version); paid != nil && paid.After(horizon) {
			horizon = time.Date(BeijingDay(*paid).Year()+1, 1, 1, 0, 0, 0, 0, BeijingLocation)
		}
	}
	return horizon
}

func earliestEffective(versions []models.BillingPriceVersion, fallback time.Time) time.Time {
	from := fallback
	for _, version := range versions {
		if version.EffectiveFrom.Before(from) {
			from = version.EffectiveFrom
		}
	}
	return from
}

func walkCommittedCycles(
	versions []models.BillingPriceVersion,
	meta map[string]clientBillingMeta,
	currency string,
	snapshots map[uint64]map[string]string,
	from, until time.Time,
) ([]committedCycle, error) {
	clients := map[string]struct{}{}
	for _, version := range versions {
		clients[version.Client] = struct{}{}
	}
	result := make([]committedCycle, 0, len(clients)*12)
	for client := range clients {
		resetDay := 1
		if item, ok := meta[client]; ok {
			resetDay = item.ResetDay
		}
		for _, start := range iterCycleStarts(resetDay, from, until) {
			end := trafficledger.NextCycleStart(start, resetDay)
			version := latestVersionForCycle(versions, client, start, end)
			if version == nil {
				continue
			}
			if paid := paidUntil(*version); paid != nil {
				if !start.Before(*paid) {
					continue
				}
				if paid.Before(end) {
					end = *paid
				}
			}
			_, monthlyNative, _, err := cycleAverageMicros(*version)
			if err != nil {
				return nil, err
			}
			amount, ok := convertLockedMicros(monthlyNative, *version, currency, snapshots)
			if !ok {
				continue
			}
			result = append(result, committedCycle{Start: start, End: end, Client: client, Version: *version, Amount: amount, NativeAmount: monthlyNative})
		}
	}
	return result, nil
}

func addCommittedCycles(
	periods map[string]*amountAccumulator,
	servers map[string]map[string]struct{},
	available map[int]struct{},
	cycles []committedCycle,
	yearSet map[int]struct{},
	monthSet map[string]struct{},
	monthly bool,
) {
	ensure := func(key string) *amountAccumulator {
		if periods[key] == nil {
			periods[key] = &amountAccumulator{}
			servers[key] = map[string]struct{}{}
		}
		return periods[key]
	}
	for _, cycle := range cycles {
		local := cycle.Start.In(BeijingLocation)
		year := local.Year()
		available[year] = struct{}{}
		if len(yearSet) > 0 {
			if _, ok := yearSet[year]; !ok {
				continue
			}
		}
		key := local.Format("2006")
		if monthly {
			key = local.Format("2006-01")
			if len(monthSet) > 0 {
				if _, ok := monthSet[key]; !ok {
					continue
				}
			}
		}
		addCategory(ensure(key), EntryTypeBaseAccrual, cycle.Amount)
		servers[key][cycle.Client] = struct{}{}
	}
}

func versionActiveOnDay(version models.BillingPriceVersion, dayStart, dayEnd time.Time) bool {
	if version.PriceMicros <= 0 || version.BillingCycleDays <= 0 || !version.CurrencyValid {
		return false
	}
	if !version.EffectiveFrom.Before(dayEnd) {
		return false
	}
	if version.EffectiveTo != nil && !version.EffectiveTo.After(dayStart) {
		return false
	}
	if version.ExpiredAt != nil && !version.ExpiredAt.After(dayStart) {
		return false
	}
	return true
}
