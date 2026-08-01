package notifier

import (
	"fmt"
	"math"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/trafficledger"
	"github.com/komari-monitor/komari/pkg/config"
	logger "github.com/komari-monitor/komari/utils/log"
	"github.com/komari-monitor/komari/utils/messageSender"
	agent_runtime "github.com/komari-monitor/komari/web/agent"
	cache "github.com/patrickmn/go-cache"
)

// trafficCache 用于记录每个客户端已触发的阈值步进，避免重复提醒
// key: "traffic:"+clientUUID, value: trafficReminderState
var trafficCache = cache.New(30*24*time.Hour, time.Hour) // 30天缓存，1小时清理

type trafficUsageSnapshot struct {
	Used  int64
	Limit int64
	Type  string
}

type trafficReminderState struct {
	Step  int
	Limit int64
	Type  string
}

func currentTrafficUsage(client models.Client, up, down int64, now time.Time) trafficUsageSnapshot {
	limit, typeName := clients.EffectiveTrafficLimit(client, now)
	return trafficUsageSnapshot{
		Used:  computeUsedByType(typeName, up, down),
		Limit: limit,
		Type:  typeName,
	}
}

// CheckTraffic 检查各客户端流量使用情况，并在达到阈值和每+5%时提醒一次；100%时额外提醒一次
// 由外部协程每分钟调用一次
func CheckTraffic() {
	// 获取最新上报与客户端配置
	reports := agent_runtime.GetLatestReport()
	if len(reports) == 0 {
		return
	}
	cfg, err := config.GetAs[float64](config.TrafficLimitPercentageKey, 80.0)
	if err != nil {
		logger.Error("notifier", "failed to get traffic limit percentage", "error", err)
	}

	if cfg <= 0 {
		return
	}

	// 起始阈值：例如 80%，非5的倍数则从上取整到最近的5的倍数，例如 83->85
	startThreshold := cfg
	if startThreshold < 0 {
		startThreshold = 0
	}
	baseStep := int(math.Ceil(startThreshold/5.0) * 5.0)
	if baseStep > 100 {
		baseStep = 100
	}

	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return
	}

	now := time.Now().UTC()
	for _, c := range allClients {
		r, ok := reports[c.UUID]
		if !ok || r == nil {
			continue
		}

		usage := currentTrafficUsage(c, r.Network.TotalUp, r.Network.TotalDown, now)
		if usage.Limit <= 0 || usage.Used <= 0 {
			continue
		}

		pct := float64(usage.Used) / float64(usage.Limit) * 100.0
		key := "traffic:" + c.UUID
		last, _ := trafficCache.Get(key)
		state, _ := last.(trafficReminderState)
		if state.Limit != usage.Limit || state.Type != usage.Type {
			state = trafficReminderState{Limit: usage.Limit, Type: usage.Type}
			trafficCache.SetDefault(key, state)
		}
		if pct < startThreshold {
			continue
		}

		// 当前所在阈值步进（5%的倍数）
		curStep := int(math.Floor(pct/5.0) * 5.0)
		if curStep < baseStep {
			curStep = baseStep
		}
		// if curStep > 100 {
		// 	curStep = 100
		// }

		// 修复：当检测到当前进度小于历史记录时，说明流量已重置，将基准归零
		if curStep < state.Step {
			state.Step = 0
		}

		if curStep > state.Step { // 只在进入新步进时提醒一次
			state.Step = curStep
			trafficCache.SetDefault(key, state)

			msg := fmt.Sprintf("used %d%% (%s / %s), type=%s", curStep, humanBytes(usage.Used), humanBytes(usage.Limit), usage.Type)
			// 发送通知（内部会检查 NotificationEnabled）
			_ = messageSender.SendEvent(models.EventMessage{
				Event:   "Traffic",
				Clients: []models.Client{c},
				Time:    time.Now().UTC(),
				Emoji:   "⚠️",
				Message: msg,
			})
		}
	}
}

func computeUsedByType(t string, up, down int64) int64 {
	return trafficledger.BillableUsage(t, up, down)
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	// KMGTPE
	prefixes := []string{"K", "M", "G", "T", "P", "E"}
	if exp >= len(prefixes) {
		exp = len(prefixes) - 1
	}
	return fmt.Sprintf("%.2f %sB", float64(b)/float64(div), prefixes[exp])
}
