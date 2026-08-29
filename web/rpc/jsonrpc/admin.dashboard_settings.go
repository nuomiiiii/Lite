package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/pkg/rpc"
)

const (
	dashboardPresetOverview   = "overview"
	dashboardPresetNetwork    = "network"
	dashboardPresetResources  = "resources"
	dashboardPresetTraffic    = "traffic"
	dashboardPresetOperations = "operations"
	dashboardPresetLite       = "lite"
	dashboardPresetCustom     = "custom"
)

const (
	dashboardModuleServerStatus      = "server_status"
	dashboardModuleTrafficSummary    = "traffic_summary"
	dashboardModuleStorageSummary    = "storage_summary"
	dashboardModuleCostCenter        = "cost_center"
	dashboardModuleResourceRanking   = "resource_ranking"
	dashboardModuleTrafficRanking    = "daily_traffic_ranking"
	dashboardModuleLatencyRanking    = "latency_ranking"
	dashboardModuleJitterRanking     = "latency_jitter_ranking"
	dashboardModulePacketLossRanking = "packet_loss_ranking"
	dashboardModuleLatencyTrend      = "latency_trend"
	dashboardModuleTrafficTrend      = "traffic_trend"
	dashboardModuleBillingTrend      = "billing_trend"
	dashboardModuleReturnRoute       = "return_route"
	dashboardModuleAlerts            = "alerts"
	dashboardModuleStorageDetail     = "storage_detail"
)

var dashboardModuleOrder = []string{
	dashboardModuleServerStatus,
	dashboardModuleTrafficSummary,
	dashboardModuleStorageSummary,
	dashboardModuleCostCenter,
	dashboardModuleResourceRanking,
	dashboardModuleTrafficRanking,
	dashboardModuleLatencyRanking,
	dashboardModuleJitterRanking,
	dashboardModulePacketLossRanking,
	dashboardModuleLatencyTrend,
	dashboardModuleTrafficTrend,
	dashboardModuleBillingTrend,
	dashboardModuleReturnRoute,
	dashboardModuleAlerts,
	dashboardModuleStorageDetail,
}

type dashboardPresetDefinition struct {
	Modules             []string
	RefreshSeconds      int
	ChartRefreshSeconds int
	RankingLimit        int
}

var dashboardPresetDefinitions = map[string]dashboardPresetDefinition{
	dashboardPresetOverview: {
		Modules: []string{
			dashboardModuleServerStatus,
			dashboardModuleTrafficSummary,
			dashboardModuleStorageSummary,
			dashboardModuleCostCenter,
			dashboardModuleLatencyTrend,
			dashboardModuleTrafficTrend,
			dashboardModuleBillingTrend,
			dashboardModuleReturnRoute,
			dashboardModuleAlerts,
			dashboardModuleStorageDetail,
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 30, RankingLimit: 5,
	},
	dashboardPresetNetwork: {
		Modules: []string{
			dashboardModuleServerStatus,
			dashboardModuleTrafficSummary,
			dashboardModuleStorageSummary,
			dashboardModuleCostCenter,
			dashboardModuleLatencyTrend,
			dashboardModuleTrafficRanking,
			dashboardModuleLatencyRanking,
			dashboardModuleJitterRanking,
			dashboardModulePacketLossRanking,
			dashboardModuleTrafficTrend,
			dashboardModuleBillingTrend,
			dashboardModuleReturnRoute,
			dashboardModuleAlerts,
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 60, RankingLimit: 5,
	},
	dashboardPresetResources: {
		Modules: []string{
			dashboardModuleServerStatus,
			dashboardModuleStorageSummary,
			dashboardModuleCostCenter,
			dashboardModuleResourceRanking,
			dashboardModuleAlerts,
			dashboardModuleStorageDetail,
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 120, RankingLimit: 5,
	},
	dashboardPresetTraffic: {
		Modules: []string{
			dashboardModuleServerStatus,
			dashboardModuleTrafficSummary,
			dashboardModuleStorageSummary,
			dashboardModuleCostCenter,
			dashboardModuleTrafficRanking,
			dashboardModuleAlerts,
			dashboardModuleTrafficTrend,
			dashboardModuleBillingTrend,
		},
		RefreshSeconds: 60, ChartRefreshSeconds: 120, RankingLimit: 5,
	},
	dashboardPresetOperations: {
		Modules: []string{
			dashboardModuleServerStatus,
			dashboardModuleTrafficSummary,
			dashboardModuleStorageSummary,
			dashboardModuleCostCenter,
			dashboardModuleAlerts,
			dashboardModuleReturnRoute,
			dashboardModuleResourceRanking,
			dashboardModuleStorageDetail,
			dashboardModuleLatencyRanking,
			dashboardModuleJitterRanking,
			dashboardModulePacketLossRanking,
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 120, RankingLimit: 5,
	},
	dashboardPresetLite: {
		Modules: []string{
			dashboardModuleServerStatus,
			dashboardModuleStorageSummary,
			dashboardModuleCostCenter,
			dashboardModuleResourceRanking,
			dashboardModuleAlerts,
			dashboardModuleStorageDetail,
		},
		RefreshSeconds: 60, ChartRefreshSeconds: 120, RankingLimit: 5,
	},
}

var validDashboardPresets = map[string]struct{}{
	dashboardPresetOverview:   {},
	dashboardPresetNetwork:    {},
	dashboardPresetResources:  {},
	dashboardPresetTraffic:    {},
	dashboardPresetOperations: {},
	dashboardPresetLite:       {},
	dashboardPresetCustom:     {},
}

var validDashboardModules = func() map[string]struct{} {
	result := make(map[string]struct{}, len(dashboardModuleOrder))
	for _, id := range dashboardModuleOrder {
		result[id] = struct{}{}
	}
	return result
}()

type dashboardModuleSetting struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Span    int    `json:"span"`
}

type dashboardSettings struct {
	Preset              string                   `json:"preset"`
	Modules             []dashboardModuleSetting `json:"modules"`
	RefreshSeconds      int                      `json:"refresh_seconds"`
	ChartRefreshSeconds int                      `json:"chart_refresh_seconds"`
	RankingLimit        int                      `json:"ranking_limit"`
	LayoutColumns       int                      `json:"layout_columns,omitempty"`
}

func init() {
	RegisterWithGroupAndMeta("getDashboardSettings", rpc.RoleAdmin, adminGetDashboardSettings, &rpc.MethodMeta{
		Name:    "admin:getDashboardSettings",
		Summary: "Get dashboard layout settings",
		Returns: "DashboardSettings",
	})
	RegisterWithGroupAndMeta("setDashboardSettings", rpc.RoleAdmin, adminSetDashboardSettings, &rpc.MethodMeta{
		Name:    "admin:setDashboardSettings",
		Summary: "Set dashboard layout settings",
		Returns: "DashboardSettings",
	})
}

func defaultDashboardSettings() dashboardSettings {
	return dashboardSettingsForPreset(dashboardPresetOverview)
}

func dashboardSettingsForPreset(preset string) dashboardSettings {
	definition, ok := dashboardPresetDefinitions[preset]
	if !ok {
		definition = dashboardPresetDefinitions[dashboardPresetOverview]
		preset = dashboardPresetOverview
	}
	enabled := make(map[string]struct{}, len(definition.Modules))
	modules := make([]dashboardModuleSetting, 0, len(dashboardModuleOrder))
	for _, id := range definition.Modules {
		enabled[id] = struct{}{}
		modules = append(modules, dashboardModuleSetting{ID: id, Enabled: true, Span: dashboardDefaultModuleSpan(id)})
	}
	for _, id := range dashboardModuleOrder {
		if _, ok := enabled[id]; !ok {
			modules = append(modules, dashboardModuleSetting{ID: id, Span: dashboardDefaultModuleSpan(id)})
		}
	}
	return dashboardSettings{
		Preset:              preset,
		Modules:             modules,
		RefreshSeconds:      definition.RefreshSeconds,
		ChartRefreshSeconds: definition.ChartRefreshSeconds,
		RankingLimit:        definition.RankingLimit,
		LayoutColumns:       dashboardGridColumns,
	}
}

func loadDashboardSettings() dashboardSettings {
	fallback := defaultDashboardSettings()
	stored, err := config.GetAs[dashboardSettings](config.DashboardSettingsKey, fallback)
	if err != nil {
		return fallback
	}
	normalized, err := normalizeDashboardSettings(stored, false)
	if err != nil {
		return fallback
	}
	return normalized
}

func adminGetDashboardSettings(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return loadDashboardSettings(), nil
}

func adminSetDashboardSettings(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	var input dashboardSettings
	if err := req.BindParams(&input); err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid dashboard settings: "+err.Error(), nil)
	}
	normalized, err := normalizeDashboardSettings(input, true)
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, "Invalid dashboard settings: "+err.Error(), nil)
	}
	if err := config.Set(config.DashboardSettingsKey, normalized); err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to save dashboard settings: "+err.Error(), nil)
	}
	actor, ip := auditActor(ctx)
	auditlog.Log(ip, actor, "update dashboard settings", "info")
	return normalized, nil
}

func normalizeDashboardSettings(input dashboardSettings, strict bool) (dashboardSettings, error) {
	defaults := defaultDashboardSettings()
	result := dashboardSettings{
		Preset:              strings.TrimSpace(input.Preset),
		RefreshSeconds:      input.RefreshSeconds,
		ChartRefreshSeconds: input.ChartRefreshSeconds,
		RankingLimit:        input.RankingLimit,
	}

	if result.Preset == "" {
		result.Preset = defaults.Preset
	}
	if _, ok := validDashboardPresets[result.Preset]; !ok {
		if strict {
			return dashboardSettings{}, fmt.Errorf("unknown preset %q", result.Preset)
		}
		result.Preset = dashboardPresetCustom
	}
	if result.Preset != dashboardPresetCustom {
		if strict {
			if input.RefreshSeconds != 0 && !dashboardRefreshAllowed(input.RefreshSeconds) {
				return dashboardSettings{}, errors.New("refresh_seconds must be 15, 30, 60, or 120")
			}
			if input.ChartRefreshSeconds != 0 && !dashboardChartRefreshAllowed(input.ChartRefreshSeconds) {
				return dashboardSettings{}, errors.New("chart_refresh_seconds must be 15, 30, 60, or 120")
			}
			if input.RankingLimit != 0 && !dashboardRankingLimitAllowed(input.RankingLimit) {
				return dashboardSettings{}, errors.New("ranking_limit must be 5, 10, 15, or 20")
			}
		}
		return dashboardSettingsForPreset(result.Preset), nil
	}

	if len(input.Modules) == 0 {
		result.Modules = defaults.Modules
	} else {
		seen := make(map[string]struct{}, len(dashboardModuleOrder))
		result.Modules = make([]dashboardModuleSetting, 0, len(dashboardModuleOrder))
		for _, module := range input.Modules {
			module.ID = strings.TrimSpace(module.ID)
			if _, ok := validDashboardModules[module.ID]; !ok {
				if strict {
					return dashboardSettings{}, fmt.Errorf("unknown module %q", module.ID)
				}
				continue
			}
			if _, duplicate := seen[module.ID]; duplicate {
				if strict {
					return dashboardSettings{}, fmt.Errorf("duplicate module %q", module.ID)
				}
				continue
			}
			if module.Span == 0 {
				module.Span = dashboardDefaultModuleSpan(module.ID)
			} else {
				module.Span = migrateDashboardModuleSpan(module.Span, input.LayoutColumns)
				if !dashboardModuleSpanAllowed(module.Span) {
					if strict {
						return dashboardSettings{}, fmt.Errorf("module %q span must be 3, 4, 6, or 12", module.ID)
					}
					module.Span = dashboardDefaultModuleSpan(module.ID)
				}
			}
			seen[module.ID] = struct{}{}
			result.Modules = append(result.Modules, module)
		}
		_, hadCostCenter := seen[dashboardModuleCostCenter]
		for _, id := range dashboardModuleOrder {
			if _, ok := seen[id]; !ok {
				if id == dashboardModuleCostCenter {
					continue
				}
				result.Modules = append(result.Modules, dashboardModuleSetting{ID: id, Span: dashboardDefaultModuleSpan(id)})
			}
		}
		if !hadCostCenter {
			result.Modules = insertCostCenterAfterStorage(result.Modules)
		}
		enableNewCostCenterModule(result.Modules, !hadCostCenter)
	}

	enabled := 0
	for _, module := range result.Modules {
		if module.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		if strict {
			return dashboardSettings{}, errors.New("at least one module must be enabled")
		}
		result.Modules[0].Enabled = true
	}

	if !dashboardRefreshAllowed(result.RefreshSeconds) {
		if strict && result.RefreshSeconds != 0 {
			return dashboardSettings{}, errors.New("refresh_seconds must be 15, 30, 60, or 120")
		}
		result.RefreshSeconds = defaults.RefreshSeconds
	}
	if !strict && result.ChartRefreshSeconds == 300 {
		result.ChartRefreshSeconds = 120
	}
	if !dashboardChartRefreshAllowed(result.ChartRefreshSeconds) {
		if strict && result.ChartRefreshSeconds != 0 {
			return dashboardSettings{}, errors.New("chart_refresh_seconds must be 15, 30, 60, or 120")
		}
		result.ChartRefreshSeconds = defaults.ChartRefreshSeconds
	}
	if !dashboardRankingLimitAllowed(result.RankingLimit) {
		if strict && result.RankingLimit != 0 {
			return dashboardSettings{}, errors.New("ranking_limit must be 5, 10, 15, or 20")
		}
		result.RankingLimit = defaults.RankingLimit
	}

	result.LayoutColumns = dashboardGridColumns
	return result, nil
}

func dashboardRefreshAllowed(value int) bool {
	return value == 15 || value == 30 || value == 60 || value == 120
}

func dashboardChartRefreshAllowed(value int) bool {
	return value == 15 || value == 30 || value == 60 || value == 120
}

func dashboardRankingLimitAllowed(value int) bool {
	return value == 5 || value == 10 || value == 15 || value == 20
}

const dashboardGridColumns = 12

func dashboardModuleSpanAllowed(value int) bool {
	return value == 3 || value == 4 || value == 6 || value == 12
}

func migrateDashboardModuleSpan(span, layoutColumns int) int {
	if layoutColumns != dashboardGridColumns && (span == 2 || span == 3 || span == 6) {
		return span * 2
	}
	return span
}

func dashboardDefaultModuleSpan(id string) int {
	switch id {
	case dashboardModuleServerStatus, dashboardModuleTrafficSummary, dashboardModuleStorageSummary, dashboardModuleCostCenter:
		return 3
	case dashboardModuleResourceRanking, dashboardModuleLatencyTrend:
		return 12
	default:
		return 6
	}
}

func insertCostCenterAfterStorage(modules []dashboardModuleSetting) []dashboardModuleSetting {
	for _, module := range modules {
		if module.ID == dashboardModuleCostCenter {
			return modules
		}
	}
	insert := dashboardModuleSetting{ID: dashboardModuleCostCenter, Span: dashboardDefaultModuleSpan(dashboardModuleCostCenter)}
	idx := len(modules)
	for i, module := range modules {
		if module.ID == dashboardModuleStorageSummary {
			idx = i + 1
			break
		}
	}
	out := make([]dashboardModuleSetting, 0, len(modules)+1)
	out = append(out, modules[:idx]...)
	out = append(out, insert)
	out = append(out, modules[idx:]...)
	return out
}

func enableNewCostCenterModule(modules []dashboardModuleSetting, newlyInserted bool) {
	summaryOn := 0
	legacyThirds := 0
	costIndex := -1
	for i, module := range modules {
		if module.ID == dashboardModuleCostCenter {
			costIndex = i
		}
		if module.ID == dashboardModuleServerStatus || module.ID == dashboardModuleTrafficSummary || module.ID == dashboardModuleStorageSummary {
			if module.Enabled {
				summaryOn++
			}
			if module.Span == 4 {
				legacyThirds++
			}
		}
	}
	if costIndex < 0 || modules[costIndex].Enabled || summaryOn < 3 {
		return
	}
	if !newlyInserted && legacyThirds < 3 {
		return
	}
	modules[costIndex].Enabled = true
	for i := range modules {
		switch modules[i].ID {
		case dashboardModuleServerStatus, dashboardModuleTrafficSummary, dashboardModuleStorageSummary, dashboardModuleCostCenter:
			if modules[i].Span == 3 || modules[i].Span == 4 {
				modules[i].Span = 3
			}
		}
	}
}
