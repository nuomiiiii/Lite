package notifier

import (
	"fmt"
	"log"
	"math"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/messageSender"
)

func CheckExpireScheduledWork() {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location()) // UTC 9AM = CST 17PM
		if now.After(next) {
			next = next.Add(24 * time.Hour)
		}
		duration := next.Sub(now)
		time.Sleep(duration)

		cfg, err := config.Get()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		clients_all, err := clients.GetAllClientBasicInfo()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		checkTime := time.Now()

		// 自动续费检查
		var renewedClients []string
		for _, client := range clients_all {
			clientExpireTime := client.ExpiredAt.ToTime()

			// 检查是否已过期或当天过期
			if clientExpireTime.Before(checkTime) || clientExpireTime.Format("2006-01-02") == checkTime.Format("2006-01-02") {
				// 计算过期时间距离创建时间的总天数，判断是否为长期账单
				// 使用一个基准日期（如系统启动时间）来计算，这里简化为判断过期时间是否超过当前时间100年
				now := time.Now()
				hundredYearsFromNow := now.AddDate(100, 0, 0)

				// 如果过期时间超过当前时间100年，视为长期/一次性账单，不续费
				if clientExpireTime.After(hundredYearsFromNow) {
					continue
				}

				// 如果有账单周期且不为0，进行自动续费
				if client.BillingCycle > 0 {
					// 计算新的过期时间
					newExpireTime := clientExpireTime.AddDate(0, 0, client.BillingCycle)

					// 更新客户端过期时间
					updates := map[string]interface{}{
						"uuid":       client.UUID,
						"expired_at": models.FromTime(newExpireTime),
					}

					err := clients.SaveClient(updates)
					if err != nil {
						log.Printf("Failed to renew client %s (%s): %v", client.Name, client.UUID, err)
						continue
					}

					renewedClients = append(renewedClients, fmt.Sprintf("%s (renewed until %s)",
						client.Name, newExpireTime.Format("2006-01-02")))

					log.Printf("Auto-renewed client: %s (%s) until %s", client.Name, client.UUID, newExpireTime.Format("2006-01-02"))
				}
			}
		}

		// 发送续费通知
		if len(renewedClients) > 0 {
			message := "The following clients have been automatically renewed: \n\n"
			for _, clientInfo := range renewedClients {
				message += fmt.Sprintf("• %s\n", clientInfo)
			}
			messageSender.SendTextMessage(message, "Komari Auto-Renewal Notification")
		}

		// 过期提醒检查（仅当启用过期通知时）
		if cfg.ExpireNotificationEnabled {
			notificationLeadDays := cfg.ExpireNotificationLeadDays

			type clientToExpireInfo struct {
				Name     string
				DaysLeft int
			}

			var clientLeadToExpire []clientToExpireInfo

			for _, client := range clients_all {
				clientExpireTime := client.ExpiredAt.ToTime()

				if clientExpireTime.Before(checkTime) {
					continue
				}

				notificationThreshold := checkTime.Add(time.Duration(notificationLeadDays) * 24 * time.Hour)

				if clientExpireTime.Before(notificationThreshold) || clientExpireTime.Equal(notificationThreshold) {
					remainingDuration := clientExpireTime.Sub(checkTime)
					daysLeft := int(math.Ceil(remainingDuration.Hours() / 24))

					clientLeadToExpire = append(clientLeadToExpire, clientToExpireInfo{
						Name:     client.Name,
						DaysLeft: daysLeft,
					})
				}
			}

			if len(clientLeadToExpire) > 0 {
				message := "The following clients are about to expire: \n\n"
				for _, clientInfo := range clientLeadToExpire {
					message += fmt.Sprintf("• %s in %d days\n", clientInfo.Name, clientInfo.DaysLeft)
				}
				messageSender.SendTextMessage(message, "Komari Expiration Notification")
			}
		}

		// 等待1秒，防止多次触发
		time.Sleep(time.Second)
	}
}
