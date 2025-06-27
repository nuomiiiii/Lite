package notification

import (
	"fmt"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/utils/messageSender"
)

func CheckExpireScheduledWork() {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 17, 0, 0, 0, now.Location())
		if now.After(next) {
			// 如果已经过了今天的17:00，则设为明天的17:00
			next = next.Add(24 * time.Hour)
		}
		duration := next.Sub(now)
		time.Sleep(duration)

		cfg, err := config.Get()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		if !cfg.ExpireNotificationEnabled {
			time.Sleep(time.Second)
			continue
		}

		notificationLeadDays := cfg.ExpireNotificationLeadDays

		clients_all, err := clients.GetAllClientBasicInfo()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		var clientLeadToExpire []string
		for _, client := range clients_all {
			if client.ExpiredAt.ToTime().Before(now) {
				continue
			}

			notificationThreshold := now.Add(time.Duration(notificationLeadDays) * 24 * time.Hour)

			if client.ExpiredAt.ToTime().Before(notificationThreshold) || client.ExpiredAt.ToTime().Equal(notificationThreshold) {
				clientLeadToExpire = append(clientLeadToExpire, client.Name)
			}
		}

		if len(clientLeadToExpire) > 0 {
			message := "The following clients are about to expire: \n"
			for i := 0; i < len(clientLeadToExpire); i++ {
				message += fmt.Sprintf("%s in %d days.\n", clientLeadToExpire[i], notificationLeadDays)
			}
			messageSender.SendTextMessage(message)
		}

		// 等待1秒，防止多次触发
		time.Sleep(time.Second)
	}
}
