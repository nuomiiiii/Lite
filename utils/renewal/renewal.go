package renewal

import (
	"fmt"
	"time"

	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/models"
	messageevent "github.com/nuomiiiii/lite/database/models/messageEvent"
	"github.com/nuomiiiii/lite/pkg/timeutil"
	"github.com/nuomiiiii/lite/utils/messageSender"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

func CheckAndAutoRenewal(client models.Client) {
	// 自动续费检查
	//type renewedClient struct {
	//	Name          string
	//	NewExpireTime time.Time
	//}
	//var renewedClients []renewedClient

	if !client.AutoRenewal {
		return
	}
	if !agent_runtime.IsPresent(client.UUID) {
		return
	}
	if client.ExpiredAt == nil {
		return
	}

	clientExpireTime := client.ExpiredAt.UTC()
	checkTime := time.Now().UTC()

	// 如果到期时间小于0002年，跳过
	if clientExpireTime.Year() < 2 {
		return
	}

	// 北京时间到达到期日当天（含当天 0 点）即续，不等到第二天或早上 9 点。
	if timeutil.BeijingDayReached(checkTime, clientExpireTime) {
		now := checkTime
		localNow := timeutil.BeijingDay(now)
		hundredYearsFromNow := localNow.AddDate(100, 0, 0)

		// 如果过期时间超过当前时间100年，视为长期/一次性账单，不续费
		if timeutil.BeijingDay(clientExpireTime).After(hundredYearsFromNow) {
			return
		}

		// 如果有账单周期且不为0，进行自动续费
		if client.BillingCycle > 0 {
			// 根据账单周期计算新的过期时间
			var newExpireTime time.Time
			billingCycle := client.BillingCycle

			// 如果服务器的过期时间太早了，那么直接设置为从当前时间算的下一个到期时间
			baseTime := timeutil.BeijingDay(clientExpireTime)
			if timeutil.BeijingDay(clientExpireTime).Before(localNow.AddDate(0, 0, -30)) {
				baseTime = localNow
			}

			if billingCycle >= 27 && billingCycle <= 32 {
				// 月度计费 - 加1个月
				newExpireTime = baseTime.AddDate(0, 1, 0)
			} else if billingCycle >= 87 && billingCycle <= 95 {
				// 季度计费 - 加3个月
				newExpireTime = baseTime.AddDate(0, 3, 0)
			} else if billingCycle >= 175 && billingCycle <= 185 {
				// 半年计费 - 加6个月
				newExpireTime = baseTime.AddDate(0, 6, 0)
			} else if billingCycle >= 360 && billingCycle <= 370 {
				// 年度计费 - 加1年
				newExpireTime = baseTime.AddDate(1, 0, 0)
			} else if billingCycle >= 720 && billingCycle <= 750 {
				// 两年计费 - 加2年
				newExpireTime = baseTime.AddDate(2, 0, 0)
			} else if billingCycle >= 1080 && billingCycle <= 1150 {
				// 三年计费 - 加3年
				newExpireTime = baseTime.AddDate(3, 0, 0)
			} else if billingCycle >= 1800 && billingCycle <= 1850 {
				// 五年计费 - 加5年
				newExpireTime = baseTime.AddDate(5, 0, 0)
			} else {
				// 其他情况，直接加上账单周期天数
				newExpireTime = baseTime.AddDate(0, 0, billingCycle)
			}

			// 更新客户端过期时间
			updates := map[string]interface{}{
				"uuid":       client.UUID,
				"expired_at": newExpireTime.In(time.UTC),
			}

			err := clients.SaveClientWithSource(updates, "renewal")
			if err != nil {
				auditlog.EventLog("renewal", fmt.Sprintf("Failed to renew client %s (%s): %v", client.Name, client.UUID, err))
				return
			}

			auditlog.EventLog("renewal", fmt.Sprintf("Auto-renewed client: %s until %s",
				client.Name, timeutil.FormatBeijingDate(newExpireTime)))

			messageSender.SendEvent(models.EventMessage{
				Event:   messageevent.Renew,
				Clients: []models.Client{client},
				Time:    time.Now().UTC(),
				Emoji:   "🔄",
				Message: fmt.Sprintf("• %s until %s\n", client.Name, timeutil.FormatBeijingDate(newExpireTime)),
			})
		}
	}

	// 发送续费通知
	// if len(renewedClients) > 0 {
	// 	message := ""
	// 	for _, clientInfo := range renewedClients {
	// 		message += fmt.Sprintf("• %s until %s\n", clientInfo.Name, clientInfo.NewExpireTime.Format("2006-01-02"))
	// 	}
	// 	messageSender.SendEvent(models.EventMessage{
	// 		Event:   messageevent.Renew,
	// 		Clients: []models.Client{client},
	// 		Time:    time.Now(),
	// 		Emoji:   "🔄",
	// 		Message: message,
	// 	})
	// }
}
