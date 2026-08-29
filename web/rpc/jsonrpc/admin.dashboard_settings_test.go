package jsonrpc

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/rpc"
	v1 "github.com/nuomiiiii/lite/protocol/v1"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDashboardSettingsDefaults(t *testing.T) {
	normalized, err := normalizeDashboardSettings(dashboardSettings{}, false)
	require.NoError(t, err)
	assert.Equal(t, dashboardPresetOverview, normalized.Preset)
	assert.Equal(t, 30, normalized.RefreshSeconds)
	assert.Equal(t, 30, normalized.ChartRefreshSeconds)
	assert.Equal(t, 5, normalized.RankingLimit)
	assert.Len(t, normalized.Modules, len(dashboardModuleOrder))
	for _, module := range normalized.Modules {
		assert.True(t, dashboardModuleSpanAllowed(module.Span))
	}
	enabled := make([]string, 0)
	for _, module := range normalized.Modules {
		if module.Enabled {
			enabled = append(enabled, module.ID)
		}
	}
	assert.Equal(t, dashboardPresetDefinitions[dashboardPresetOverview].Modules, enabled)
}

func TestNormalizeDashboardSettingsPreservesOrderAndAppendsMissingModules(t *testing.T) {
	normalized, err := normalizeDashboardSettings(dashboardSettings{
		Preset: dashboardPresetCustom,
		Modules: []dashboardModuleSetting{
			{ID: dashboardModuleAlerts, Enabled: true},
			{ID: dashboardModuleServerStatus, Enabled: true},
		},
		RefreshSeconds:      15,
		ChartRefreshSeconds: 15,
		RankingLimit:        20,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, 15, normalized.RefreshSeconds)
	assert.Equal(t, 15, normalized.ChartRefreshSeconds)
	assert.Equal(t, 20, normalized.RankingLimit)
	assert.Equal(t, dashboardModuleAlerts, normalized.Modules[0].ID)
	assert.Equal(t, dashboardModuleServerStatus, normalized.Modules[1].ID)
	assert.Len(t, normalized.Modules, len(dashboardModuleOrder))
	for _, module := range normalized.Modules[2:] {
		assert.False(t, module.Enabled)
	}
}

func TestNormalizeDashboardSettingsPreservesCustomModuleSpan(t *testing.T) {
	normalized, err := normalizeDashboardSettings(dashboardSettings{
		Preset: dashboardPresetCustom,
		Modules: []dashboardModuleSetting{
			{ID: dashboardModuleLatencyTrend, Enabled: true, Span: 6},
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 120, RankingLimit: 5,
		LayoutColumns: dashboardGridColumns,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, 6, normalized.Modules[0].Span)
	assert.Equal(t, dashboardGridColumns, normalized.LayoutColumns)
}

func TestNormalizeDashboardSettingsMigratesLegacySixColumnSpans(t *testing.T) {
	legacy, err := normalizeDashboardSettings(dashboardSettings{
		Preset: dashboardPresetCustom,
		Modules: []dashboardModuleSetting{
			{ID: dashboardModuleLatencyTrend, Enabled: true, Span: 3},
			{ID: dashboardModuleServerStatus, Enabled: true, Span: 2},
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 120, RankingLimit: 5,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, 6, legacy.Modules[0].Span)
	assert.Equal(t, 4, legacy.Modules[1].Span)
	assert.Equal(t, dashboardGridColumns, legacy.LayoutColumns)

	quarter, err := normalizeDashboardSettings(dashboardSettings{
		Preset: dashboardPresetCustom,
		Modules: []dashboardModuleSetting{
			{ID: dashboardModuleCostCenter, Enabled: true, Span: 3},
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 120, RankingLimit: 5,
		LayoutColumns: dashboardGridColumns,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, 3, quarter.Modules[0].Span)
}

func TestNormalizeDashboardSettingsUnlocksCostCenterForLegacySummaries(t *testing.T) {
	normalized, err := normalizeDashboardSettings(dashboardSettings{
		Preset: dashboardPresetCustom,
		Modules: []dashboardModuleSetting{
			{ID: dashboardModuleServerStatus, Enabled: true, Span: 4},
			{ID: dashboardModuleTrafficSummary, Enabled: true, Span: 4},
			{ID: dashboardModuleStorageSummary, Enabled: true, Span: 4},
			{ID: dashboardModuleResourceRanking, Enabled: true, Span: 12},
			{ID: dashboardModuleCostCenter, Enabled: true, Span: 3},
		},
		RefreshSeconds: 30, ChartRefreshSeconds: 30, RankingLimit: 5,
		LayoutColumns: dashboardGridColumns,
	}, false)
	require.NoError(t, err)
	ids := make([]string, 0, 5)
	for _, module := range normalized.Modules {
		if module.Enabled {
			ids = append(ids, module.ID)
		}
	}
	assert.Equal(t, []string{
		dashboardModuleServerStatus,
		dashboardModuleTrafficSummary,
		dashboardModuleStorageSummary,
		dashboardModuleResourceRanking,
		dashboardModuleCostCenter,
	}, ids[:5])
	assert.Equal(t, 4, normalized.Modules[0].Span)
	assert.True(t, normalized.Modules[4].Enabled)
}

func TestNormalizeDashboardSettingsRejectsUnsafeIntervals(t *testing.T) {
	_, err := normalizeDashboardSettings(dashboardSettings{
		Preset:              dashboardPresetOverview,
		Modules:             []dashboardModuleSetting{{ID: dashboardModuleServerStatus, Enabled: true}},
		RefreshSeconds:      5,
		ChartRefreshSeconds: 30,
		RankingLimit:        100,
	}, true)
	require.Error(t, err)
}

func TestDashboardRefreshIntervalsAndLegacyChartMigration(t *testing.T) {
	for _, value := range []int{15, 30, 60, 120} {
		assert.True(t, dashboardRefreshAllowed(value), value)
		assert.True(t, dashboardChartRefreshAllowed(value), value)
	}
	for _, value := range []int{0, 5, 300} {
		assert.False(t, dashboardRefreshAllowed(value), value)
		assert.False(t, dashboardChartRefreshAllowed(value), value)
	}

	legacy, err := normalizeDashboardSettings(dashboardSettings{
		Preset: dashboardPresetCustom,
		Modules: []dashboardModuleSetting{
			{ID: dashboardModuleLatencyTrend, Enabled: true},
		},
		RefreshSeconds:      30,
		ChartRefreshSeconds: 300,
		RankingLimit:        5,
	}, false)
	require.NoError(t, err)
	assert.Equal(t, 120, legacy.ChartRefreshSeconds)

	_, err = normalizeDashboardSettings(dashboardSettings{
		Preset: dashboardPresetCustom,
		Modules: []dashboardModuleSetting{
			{ID: dashboardModuleLatencyTrend, Enabled: true},
		},
		RefreshSeconds:      30,
		ChartRefreshSeconds: 300,
		RankingLimit:        5,
	}, true)
	require.Error(t, err)
}

func TestDashboardRankingLimitAllowedValues(t *testing.T) {
	for _, value := range []int{5, 10, 15, 20} {
		assert.True(t, dashboardRankingLimitAllowed(value), value)
	}
	for _, value := range []int{0, 1, 25, 100} {
		assert.False(t, dashboardRankingLimitAllowed(value), value)
	}
}

func TestDashboardResourceRankingUsesLatestReportsAndBoundedTopN(t *testing.T) {
	clients := make([]models.Client, 0, 7)
	for index := 0; index < 7; index++ {
		uuid := "dashboard-ranking-" + string(rune('a'+index))
		clients = append(clients, models.Client{UUID: uuid, Name: "node-" + string(rune('a'+index))})
		agent_runtime.RecordReport(v1.Report{
			UUID:      uuid,
			CPU:       v1.CPUReport{Usage: float64(index * 10)},
			Ram:       v1.RamReport{Used: int64(index + 1), Total: 10},
			Disk:      v1.DiskReport{Used: int64(7 - index), Total: 10},
			UpdatedAt: time.Now().UTC(),
		})
		defer agent_runtime.DeleteLatestReport(uuid)
	}

	result := buildDashboardResources(clients, 5)
	require.Len(t, result.CPU, 5)
	require.Len(t, result.Memory, 5)
	require.Len(t, result.Disk, 5)
	assert.Equal(t, "node-g", result.CPU[0].Name)
	assert.Equal(t, "node-g", result.Memory[0].Name)
	assert.Equal(t, "node-a", result.Disk[0].Name)
}

func TestParseDashboardSectionsDefaultsAndFilters(t *testing.T) {
	sections, limit := parseDashboardSummaryRequest(nil)
	assert.Equal(t, dashboardSectionAll, sections)
	assert.Equal(t, 5, limit)

	req := &rpc.JsonRpcRequest{Params: map[string]any{
		"sections": "servers,resources,unknown",
		"limit":    "20",
	}}
	sections, limit = parseDashboardSummaryRequest(req)
	assert.Equal(t, dashboardSectionServers|dashboardSectionResources, sections)
	assert.Equal(t, 20, limit)

	chartSections, chartLimit := parseDashboardChartRequest(&rpc.JsonRpcRequest{Params: map[string]any{
		"sections": "traffic,latency,unknown",
		"limit":    "15",
	}})
	assert.Equal(t, dashboardChartTraffic|dashboardChartLatency, chartSections)
	assert.Equal(t, 15, chartLimit)

	jitterSections, _ := parseDashboardChartRequest(&rpc.JsonRpcRequest{Params: map[string]any{
		"sections": "latency_jitter",
	}})
	assert.Equal(t, dashboardChartLatencyJitter, jitterSections)

	packetLossSections, _ := parseDashboardChartRequest(&rpc.JsonRpcRequest{Params: map[string]any{
		"sections": "packet_loss",
	}})
	assert.Equal(t, dashboardChartPacketLoss, packetLossSections)
}
