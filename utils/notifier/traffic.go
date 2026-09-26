package notifier

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"github.com/nuomiiiii/lite/pkg/config"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/utils/messageSender"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
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

// nextTrafficReminderPercent reports the reminder mark for the current usage.
// The first mark is the configured start percentage. Later marks advance by step.
// Reaching the quota reports 100 even when that is not on the step grid.
func nextTrafficReminderPercent(usagePercent, startThreshold, step float64) (int, bool) {
	if startThreshold <= 0 || usagePercent < startThreshold {
		return 0, false
	}
	if step < 1 || step > 100 {
		step = 5
	}
	if usagePercent >= 100 {
		return 100, true
	}
	steps := int(math.Floor((usagePercent-startThreshold)/step + 1e-9))
	mark := int(math.Floor(startThreshold + float64(steps)*step + 1e-9))
	if mark < 1 {
		mark = 1
	}
	if mark > 99 {
		mark = 99
	}
	return mark, true
}

func currentTrafficUsage(client models.Client, up, down int64, now time.Time) trafficUsageSnapshot {
	limit, typeName := clients.EffectiveTrafficLimit(client, now)
	return trafficUsageSnapshot{
		Used:  computeUsedByType(typeName, up, down),
		Limit: limit,
		Type:  typeName,
	}
}

// CheckTraffic 检查各客户端流量使用情况。用量达到起始比例时提醒一次，之后每增加一个提醒幅度再提醒一次；满额时再提醒一次。
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

	startThreshold := cfg
	if startThreshold < 0 {
		startThreshold = 0
	}
	step, stepErr := config.GetAs[float64](config.TrafficReminderStepKey, 5.0)
	if stepErr != nil || step < 1 || step > 100 {
		step = 5
	}

	allClients, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return
	}

	now := time.Now().UTC()
	calibrated, err := trafficledger.CurrentCalibratedCycleUsages(context.Background(), dbcore.GetDBInstance(), now)
	if err != nil {
		logger.Error("notifier", "failed to read calibrated traffic usage", "error", err)
		calibrated = map[string]trafficledger.Usage{}
	}
	for _, c := range allClients {
		r, ok := reports[c.UUID]
		if !ok || r == nil {
			continue
		}

		up, down := r.Network.TotalUp, r.Network.TotalDown
		if value, ok := calibrated[c.UUID]; ok {
			up, down = value.Up, value.Down
		}
		usage := currentTrafficUsage(c, up, down, now)
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
		curStep, reached := nextTrafficReminderPercent(pct, startThreshold, step)
		if !reached {
			continue
		}

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
