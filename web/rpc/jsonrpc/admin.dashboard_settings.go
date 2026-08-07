package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/pkg/config"
	"github.com/komari-monitor/komari/pkg/rpc"
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
			} else if !dashboardModuleSpanAllowed(module.Span) {
				if strict {
					return dashboardSettings{}, fmt.Errorf("module %q span must be 2, 3, or 6", module.ID)
				}
				module.Span = dashboardDefaultModuleSpan(module.ID)
			}
			seen[module.ID] = struct{}{}
			result.Modules = append(result.Modules, module)
		}
		for _, id := range dashboardModuleOrder {
			if _, ok := seen[id]; !ok {
				result.Modules = append(result.Modules, dashboardModuleSetting{ID: id, Span: dashboardDefaultModuleSpan(id)})
			}
		}
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

func dashboardModuleSpanAllowed(value int) bool {
	return value == 2 || value == 3 || value == 6
}

func dashboardDefaultModuleSpan(id string) int {
	switch id {
	case dashboardModuleServerStatus, dashboardModuleTrafficSummary, dashboardModuleStorageSummary:
		return 2
	case dashboardModuleResourceRanking, dashboardModuleLatencyTrend:
		return 6
	default:
		return 3
	}
}
