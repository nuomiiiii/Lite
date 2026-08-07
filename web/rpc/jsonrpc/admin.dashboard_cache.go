package jsonrpc

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"golang.org/x/sync/singleflight"
)

const dashboardCacheMaxEntries = 4

type dashboardCacheEntry[T any] struct {
	value T
	at    time.Time
}

// dashboardModuleCache stores only final module responses. The mutex protects
// its bounded metadata map and is never held while database work is running.
type dashboardModuleCache[T any] struct {
	mu      sync.Mutex
	entries map[string]dashboardCacheEntry[T]
	flight  singleflight.Group
}

func (cache *dashboardModuleCache[T]) get(
	ctx context.Context,
	now time.Time,
	key string,
	ttl time.Duration,
	load func() (T, error),
) (T, error) {
	if value, ok := cache.current(now, key, ttl); ok {
		return value, nil
	}

	result := cache.flight.DoChan(key, func() (any, error) {
		if value, ok := cache.current(now, key, ttl); ok {
			return value, nil
		}
		value, err := load()
		if err != nil {
			return nil, err
		}
		cache.store(now, key, value)
		return value, nil
	})

	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case completed := <-result:
		if completed.Err != nil {
			var zero T
			return zero, completed.Err
		}
		return completed.Val.(T), nil
	}
}

func (cache *dashboardModuleCache[T]) current(now time.Time, key string, ttl time.Duration) (T, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[key]
	if !ok || now.Sub(entry.at) >= ttl {
		var zero T
		return zero, false
	}
	return entry.value, true
}

func (cache *dashboardModuleCache[T]) store(now time.Time, key string, value T) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]dashboardCacheEntry[T], dashboardCacheMaxEntries)
	}
	if _, exists := cache.entries[key]; !exists && len(cache.entries) >= dashboardCacheMaxEntries {
		oldestKey := ""
		oldestAt := now
		for candidate, entry := range cache.entries {
			if oldestKey == "" || entry.at.Before(oldestAt) {
				oldestKey = candidate
				oldestAt = entry.at
			}
		}
		delete(cache.entries, oldestKey)
	}
	cache.entries[key] = dashboardCacheEntry[T]{value: value, at: now}
}

type dashboardStorageModule struct {
	database databaseStatusResponse
	storage  dashboardStorageSummary
}

var (
	dashboardServersModuleCache    dashboardModuleCache[dashboardServerSummary]
	dashboardResourcesModuleCache  dashboardModuleCache[dashboardResourceSummary]
	dashboardStorageModuleCache    dashboardModuleCache[dashboardStorageModule]
	dashboardRouteModuleCache      dashboardModuleCache[dashboardReturnRouteSummary]
	dashboardAlertsModuleCache     dashboardModuleCache[dashboardAlertSummaries]
	dashboardTrafficModuleCache    dashboardModuleCache[dashboardTrafficSummary]
	dashboardLatencyModuleCache    dashboardModuleCache[dashboardLatencySummary]
	dashboardJitterModuleCache     dashboardModuleCache[[]dashboardLatencyJitterRankItem]
	dashboardPacketLossModuleCache dashboardModuleCache[dashboardPacketLossSummary]
)

func buildDashboardCached(ctx context.Context, now time.Time, sections dashboardSummarySections, rankingLimit int, cacheTTL time.Duration) (dashboardResponse, error) {
	result := dashboardResponse{GeneratedAt: now}
	needsClients := sections&(dashboardSectionServers|dashboardSectionResources|dashboardSectionAlerts) != 0
	var clientList []models.Client
	var err error
	if needsClients {
		clientList, err = clients.GetAllClientBasicInfo()
		if err != nil {
			return dashboardResponse{}, fmt.Errorf("list dashboard clients: %w", err)
		}
	}

	if sections&dashboardSectionServers != 0 {
		result.Servers, err = dashboardServersModuleCache.get(ctx, now, "servers", cacheTTL,
			func() (dashboardServerSummary, error) { return buildDashboardServers(clientList), nil })
		if err != nil {
			return dashboardResponse{}, err
		}
	}
	if sections&dashboardSectionResources != 0 {
		key := strconv.Itoa(rankingLimit)
		result.Resources, err = dashboardResourcesModuleCache.get(ctx, now, key, cacheTTL,
			func() (dashboardResourceSummary, error) {
				return buildDashboardResources(clientList, rankingLimit), nil
			})
		if err != nil {
			return dashboardResponse{}, err
		}
	}
	if sections&dashboardSectionStorage != 0 {
		storage, loadErr := dashboardStorageModuleCache.get(ctx, now, "storage", cacheTTL,
			func() (dashboardStorageModule, error) {
				main := mainDatabaseStatus()
				monitoring := monitoringDatabaseStatus(ctx)
				legacySize := int64(0)
				if main.Size != nil {
					legacySize = *main.Size
				}
				return dashboardStorageModule{
					database: databaseStatusResponse{
						Type: main.Driver, Size: legacySize, Main: main, Monitoring: monitoring,
						LocalTotal: localDatabaseTotal(main, monitoring),
					},
					storage: buildDashboardStorage(ctx, main, monitoring),
				}, nil
			})
		if loadErr != nil {
			return dashboardResponse{}, loadErr
		}
		result.Database = storage.database
		result.Storage = storage.storage
	}
	if sections&dashboardSectionReturnRoute != 0 {
		result.ReturnRoute, err = dashboardRouteModuleCache.get(ctx, now, "return_route", cacheTTL,
			func() (dashboardReturnRouteSummary, error) { return buildDashboardReturnRoute(), nil })
		if err != nil {
			return dashboardResponse{}, err
		}
	}
	if sections&dashboardSectionAlerts != 0 {
		result.Alerts, err = dashboardAlertsModuleCache.get(ctx, now, "alerts", cacheTTL,
			func() (dashboardAlertSummaries, error) { return buildDashboardAlerts(clientList, now), nil })
		if err != nil {
			return dashboardResponse{}, err
		}
	}
	return result, nil
}

func buildDashboardChartsCached(ctx context.Context, now time.Time, sections dashboardChartSections, rankingLimit int, cacheTTL time.Duration) dashboardChartsResponse {
	result := dashboardChartsResponse{GeneratedAt: now}
	if sections == 0 {
		return result
	}
	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		message := fmt.Sprintf("list dashboard clients: %v", err)
		if sections&dashboardChartTraffic != 0 {
			result.Traffic.Error = message
		}
		if sections&dashboardChartLatency != 0 {
			result.Latency.Error = message
		}
		if sections&dashboardChartLatencyJitter != 0 {
			result.Latency.JitterError = message
		}
		if sections&dashboardChartPacketLoss != 0 {
			result.PacketLoss.Error = message
		}
		return result
	}
	key := strconv.Itoa(rankingLimit)
	if sections&dashboardChartTraffic != 0 {
		result.Traffic, err = dashboardTrafficModuleCache.get(ctx, now, key, cacheTTL,
			func() (dashboardTrafficSummary, error) {
				return loadDashboardTraffic(ctx, clientList, now, rankingLimit)
			})
		if err != nil {
			result.Traffic = dashboardTrafficSummary{Error: err.Error()}
		}
	}
	if sections&dashboardChartLatency != 0 {
		result.Latency, err = dashboardLatencyModuleCache.get(ctx, now, key, cacheTTL,
			func() (dashboardLatencySummary, error) {
				return loadDashboardLatency(ctx, clientList, now, rankingLimit)
			})
		if err != nil {
			result.Latency.Error = err.Error()
		}
	}
	if sections&dashboardChartLatencyJitter != 0 {
		result.Latency.JitterRanking, err = dashboardJitterModuleCache.get(ctx, now, key, cacheTTL,
			func() ([]dashboardLatencyJitterRankItem, error) {
				return loadDashboardLatencyJitter(ctx, clientList, now, rankingLimit)
			})
		if err != nil {
			result.Latency.JitterError = err.Error()
		}
	}
	if sections&dashboardChartPacketLoss != 0 {
		result.PacketLoss, err = dashboardPacketLossModuleCache.get(ctx, now, key, cacheTTL,
			func() (dashboardPacketLossSummary, error) {
				return loadDashboardPacketLoss(ctx, clientList, now, rankingLimit)
			})
		if err != nil {
			result.PacketLoss.Error = err.Error()
		}
	}
	return result
}
