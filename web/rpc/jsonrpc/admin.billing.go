package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/billing"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/pkg/rpc"
	"gorm.io/gorm"
)

func init() {
	RegisterWithGroupAndMeta("getBillingOverview", rpc.RoleAdmin, adminGetBillingOverview, &rpc.MethodMeta{Name: "admin:getBillingOverview", Summary: "Get the billing center overview"})
	RegisterWithGroupAndMeta("getBillingServers", rpc.RoleAdmin, adminGetBillingServers, &rpc.MethodMeta{Name: "admin:getBillingServers", Summary: "List server billing terms and totals"})
	RegisterWithGroupAndMeta("getBillingMonthly", rpc.RoleAdmin, adminGetBillingMonthly, &rpc.MethodMeta{Name: "admin:getBillingMonthly", Summary: "List monthly billing periods"})
	RegisterWithGroupAndMeta("getBillingYearly", rpc.RoleAdmin, adminGetBillingYearly, &rpc.MethodMeta{Name: "admin:getBillingYearly", Summary: "List yearly billing periods"})
	RegisterWithGroupAndMeta("getBillingEntries", rpc.RoleAdmin, adminGetBillingEntries, &rpc.MethodMeta{Name: "admin:getBillingEntries", Summary: "List immutable billing entries"})
	RegisterWithGroupAndMeta("createBillingTrafficReset", rpc.RoleAdmin, adminCreateBillingTrafficReset, &rpc.MethodMeta{Name: "admin:createBillingTrafficReset", Summary: "Record a traffic reset cost"})
	RegisterWithGroupAndMeta("createBillingIPChange", rpc.RoleAdmin, adminCreateBillingIPChange, &rpc.MethodMeta{Name: "admin:createBillingIPChange", Summary: "Record an IP change cost"})
	RegisterWithGroupAndMeta("createBillingOneTimeFee", rpc.RoleAdmin, adminCreateBillingOneTimeFee, &rpc.MethodMeta{Name: "admin:createBillingOneTimeFee", Summary: "Record a one-time fee"})
	RegisterWithGroupAndMeta("voidBillingEntry", rpc.RoleAdmin, adminVoidBillingEntry, &rpc.MethodMeta{Name: "admin:voidBillingEntry", Summary: "Void a billing entry with a reversal"})
}

type billingListParams struct {
	Currency         string `json:"currency"`
	Q                string `json:"q"`
	NativeCurrencies string `json:"native_currencies"`
	Regions          string `json:"regions"`
	Groups           string `json:"groups"`
	Expiry           string `json:"expiry"`
	Years            string `json:"years"`
	Months           string `json:"months"`
	Clients          string `json:"clients"`
	Types            string `json:"types"`
	Page             string `json:"page"`
	PageSize         string `json:"page_size"`
	Client           string `json:"client"`
	From             string `json:"from"`
	To               string `json:"to"`
}

func adminGetBillingOverview(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params billingListParams
	if err := req.BindParams(&params); err != nil {
		return nil, invalidBillingParams(err)
	}
	result, err := billing.GetOverview(ctx, dbcore.GetDBInstance(), params.Currency, time.Now().UTC())
	if err != nil {
		return nil, billingRPCError(err)
	}
	return result, nil
}

func adminGetBillingServers(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params billingListParams
	if err := req.BindParams(&params); err != nil {
		return nil, invalidBillingParams(err)
	}
	page, pageSize, err := billingPageParams(params.Page, params.PageSize)
	if err != nil {
		return nil, invalidBillingParams(err)
	}
	expiry, err := optionalPositiveInt(params.Expiry)
	if err != nil {
		return nil, invalidBillingParams(fmt.Errorf("expiry: %w", err))
	}
	result, err := billing.GetServers(ctx, dbcore.GetDBInstance(), billing.ServerQuery{
		Currency: params.Currency, Search: params.Q, NativeCurrencies: splitCSV(params.NativeCurrencies),
		Regions: splitCSV(params.Regions), Groups: splitCSV(params.Groups),
		ExpiryDays: expiry, Page: page, PageSize: pageSize, Now: time.Now().UTC(),
	})
	if err != nil {
		return nil, billingRPCError(err)
	}
	return result, nil
}

func adminGetBillingMonthly(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	query, rpcErr := bindPeriodQuery(req)
	if rpcErr != nil {
		return nil, rpcErr
	}
	result, err := billing.GetMonthly(ctx, dbcore.GetDBInstance(), query)
	if err != nil {
		return nil, billingRPCError(err)
	}
	return result, nil
}

func adminGetBillingYearly(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	query, rpcErr := bindPeriodQuery(req)
	if rpcErr != nil {
		return nil, rpcErr
	}
	result, err := billing.GetYearly(ctx, dbcore.GetDBInstance(), query)
	if err != nil {
		return nil, billingRPCError(err)
	}
	return result, nil
}

func bindPeriodQuery(req *rpc.JsonRpcRequest) (billing.PeriodQuery, *rpc.JsonRpcError) {
	var params billingListParams
	if err := req.BindParams(&params); err != nil {
		return billing.PeriodQuery{}, invalidBillingParams(err)
	}
	page, pageSize, err := billingPageParams(params.Page, params.PageSize)
	if err != nil {
		return billing.PeriodQuery{}, invalidBillingParams(err)
	}
	years, err := parseYears(params.Years)
	if err != nil {
		return billing.PeriodQuery{}, invalidBillingParams(err)
	}
	months, err := parseMonths(params.Months)
	if err != nil {
		return billing.PeriodQuery{}, invalidBillingParams(err)
	}
	return billing.PeriodQuery{
		Currency: params.Currency, Years: years, Months: months, Clients: splitCSV(params.Clients), Types: splitCSV(params.Types),
		NativeCurrencies: splitCSV(params.NativeCurrencies), Page: page, PageSize: pageSize, Now: time.Now().UTC(),
	}, nil
}

func adminGetBillingEntries(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params billingListParams
	if err := req.BindParams(&params); err != nil {
		return nil, invalidBillingParams(err)
	}
	page, pageSize, err := billingPageParams(params.Page, params.PageSize)
	if err != nil {
		return nil, invalidBillingParams(err)
	}
	if err := validateBillingDateRange(params.From, params.To); err != nil {
		return nil, invalidBillingParams(err)
	}
	result, err := billing.GetEntries(ctx, dbcore.GetDBInstance(), billing.EntryQuery{
		Currency: params.Currency, Client: params.Client, From: params.From, To: params.To,
		Types: splitCSV(params.Types), Q: params.Q, Page: page, PageSize: pageSize, Now: time.Now().UTC(),
	})
	if err != nil {
		return nil, billingRPCError(err)
	}
	return result, nil
}

func adminCreateBillingTrafficReset(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID           string `json:"uuid"`
		Amount         string `json:"amount"`
		Currency       string `json:"currency"`
		OccurredAt     string `json:"occurred_at"`
		Note           string `json:"note"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, invalidBillingParams(err)
	}
	var occurredAt time.Time
	var err error
	if strings.TrimSpace(params.OccurredAt) != "" {
		occurredAt, err = time.Parse(time.RFC3339Nano, params.OccurredAt)
		if err != nil {
			return nil, invalidBillingParams(fmt.Errorf("occurred_at must be RFC3339 with a timezone"))
		}
	}
	actor, ip := auditActor(ctx)
	entry, err := billing.CreateTrafficResetEntry(ctx, dbcore.GetDBInstance(), billing.TrafficResetInput{
		Client: params.UUID, Amount: params.Amount, Currency: params.Currency, OccurredAt: occurredAt,
		Note: params.Note, IdempotencyKey: params.IdempotencyKey, Operator: actor,
	})
	if err != nil {
		return nil, billingRPCError(err)
	}
	auditlog.Log(ip, actor, fmt.Sprintf("record traffic reset cost:%s:%d", params.UUID, entry.ID), "info")
	return entry, nil
}

func adminCreateBillingIPChange(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID           string `json:"uuid"`
		Amount         string `json:"amount"`
		Currency       string `json:"currency"`
		Note           string `json:"note"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, invalidBillingParams(err)
	}
	actor, ip := auditActor(ctx)
	entry, err := billing.CreateIPChangeEntry(ctx, dbcore.GetDBInstance(), billing.TrafficResetInput{
		Client: params.UUID, Amount: params.Amount, Currency: params.Currency,
		Note: params.Note, IdempotencyKey: params.IdempotencyKey, Operator: actor,
	})
	if err != nil {
		return nil, billingRPCError(err)
	}
	auditlog.Log(ip, actor, fmt.Sprintf("record ip change cost:%s:%d", params.UUID, entry.ID), "info")
	return entry, nil
}

func adminCreateBillingOneTimeFee(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		UUID           string `json:"uuid"`
		Amount         string `json:"amount"`
		Currency       string `json:"currency"`
		Note           string `json:"note"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, invalidBillingParams(err)
	}
	actor, ip := auditActor(ctx)
	entry, err := billing.CreateOneTimeFeeEntry(ctx, dbcore.GetDBInstance(), billing.TrafficResetInput{
		Client: params.UUID, Amount: params.Amount, Currency: params.Currency,
		Note: params.Note, IdempotencyKey: params.IdempotencyKey, Operator: actor,
	})
	if err != nil {
		return nil, billingRPCError(err)
	}
	auditlog.Log(ip, actor, fmt.Sprintf("record one-time fee:%s:%d", params.UUID, entry.ID), "info")
	return entry, nil
}

func adminVoidBillingEntry(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var params struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := req.BindParams(&params); err != nil {
		return nil, invalidBillingParams(err)
	}
	id, err := strconv.ParseUint(params.ID, 10, 64)
	if err != nil || id == 0 {
		return nil, invalidBillingParams(fmt.Errorf("id must be a positive integer"))
	}
	actor, ip := auditActor(ctx)
	entry, err := billing.VoidEntry(ctx, dbcore.GetDBInstance(), id, params.Reason, actor)
	if err != nil {
		return nil, billingRPCError(err)
	}
	auditlog.Log(ip, actor, fmt.Sprintf("void billing entry:%d", id), "warn")
	return entry, nil
}

func billingPageParams(pageValue, sizeValue string) (int, int, error) {
	page, size := 1, 10
	var err error
	if pageValue != "" {
		page, err = strconv.Atoi(pageValue)
		if err != nil || page < 1 {
			return 0, 0, fmt.Errorf("page must be a positive integer")
		}
	}
	if sizeValue != "" {
		size, err = strconv.Atoi(sizeValue)
		if err != nil || size < 1 || size > 100 {
			return 0, 0, fmt.Errorf("page_size must be between 1 and 100")
		}
	}
	return page, size, nil
}

func optionalPositiveInt(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return parsed, nil
}

func parseYears(value string) ([]int, error) {
	parts := splitCSV(value)
	years := make([]int, 0, len(parts))
	seen := map[int]struct{}{}
	for _, part := range parts {
		year, err := strconv.Atoi(part)
		if err != nil || year < 1 || year > 9999 {
			return nil, fmt.Errorf("years contains an invalid year")
		}
		if _, ok := seen[year]; !ok {
			seen[year] = struct{}{}
			years = append(years, year)
		}
	}
	return years, nil
}

func parseMonths(value string) ([]string, error) {
	parts := splitCSV(value)
	months := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		parsed, err := time.ParseInLocation("2006-01", part, billing.BeijingLocation)
		if err != nil {
			return nil, fmt.Errorf("months contains an invalid month")
		}
		month := parsed.Format("2006-01")
		if _, ok := seen[month]; ok {
			continue
		}
		seen[month] = struct{}{}
		months = append(months, month)
	}
	return months, nil
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}

func validateBillingDateRange(from, to string) error {
	for name, value := range map[string]string{"from": from, "to": to} {
		if value == "" {
			continue
		}
		if _, err := time.ParseInLocation(time.DateOnly, value, billing.BeijingLocation); err != nil {
			return fmt.Errorf("%s must use YYYY-MM-DD", name)
		}
	}
	if from != "" && to != "" && from > to {
		return fmt.Errorf("from must not be after to")
	}
	return nil
}

func invalidBillingParams(err error) *rpc.JsonRpcError {
	return rpc.MakeError(rpc.InvalidParams, "Invalid billing request: "+err.Error(), nil)
}

func billingRPCError(err error) *rpc.JsonRpcError {
	if errors.Is(err, billing.ErrInvalidInput) || errors.Is(err, gorm.ErrRecordNotFound) {
		return invalidBillingParams(err)
	}
	return rpc.MakeError(rpc.InternalError, err.Error(), nil)
}
