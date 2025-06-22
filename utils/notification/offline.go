package notification

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/messageSender"
)

var (
	pendingOffline = make(map[string]time.Time)
	mu             sync.Mutex
)

// OfflineNotification sends an offline notification for the client if enabled and not sent in grace period
func OfflineNotification(clientID string) {
	client, err := clients.GetClientByUUID(clientID)
	if err != nil {
		return
	}
	now := time.Now()

	conf, _ := config.Get()
	if !conf.NotificationEnabled {
		return
	}

	noti_conf := models.OfflineNotification{
		Client: clientID,
	}

	db := dbcore.GetDBInstance()
	err = db.Model(&models.OfflineNotification{}).Where("client = ?", clientID).FirstOrCreate(&noti_conf).Error
	if err != nil {
		log.Println("Failed to get or create offline notification config:", err)
		return
	}
	if !noti_conf.Enable {
		return
	}

	gracePeriod := time.Duration(noti_conf.GracePeriod) * time.Second
	if gracePeriod <= 0 {
		gracePeriod = 5 * time.Minute // default grace period if not set
	}

	mu.Lock()
	if _, exists := pendingOffline[clientID]; exists {
		mu.Unlock()
		return
	}
	pendingOffline[clientID] = now
	mu.Unlock()

	go func(start time.Time) {
		time.Sleep(gracePeriod)
		mu.Lock()
		defer mu.Unlock()
		// if no longer pending (client reconnected), skip
		if ts, ok := pendingOffline[clientID]; !ok || !ts.Equal(start) {
			return
		}
		// record notification time and clear pending
		delete(pendingOffline, clientID)

		message := fmt.Sprintf("🔴%s is offline", client.Name)
		go func(msg string) {
			if err := messageSender.SendTextMessage(msg); err != nil {
				log.Println("Failed to send offline notification:", err)
			}
		}(message)
		_ = db.Model(&models.OfflineNotification{}).Where("client = ?", clientID).Update("last_notified", now)
	}(now)
}

// OnlineNotification sends an online notification for the client if enabled
func OnlineNotification(clientID string) {
	client, err := clients.GetClientByUUID(clientID)
	if err != nil {
		return
	}
	noti_conf := models.OfflineNotification{
		Client: clientID,
	}

	db := dbcore.GetDBInstance()
	err = db.Model(&models.OfflineNotification{}).Where("client = ?", clientID).FirstOrCreate(&noti_conf).Error
	if err != nil {
		log.Println("Failed to get or create offline notification config:", err)
		return
	}
	if !noti_conf.Enable {
		return
	}
	// clear any pending offline debounce
	mu.Lock()
	delete(pendingOffline, clientID)
	mu.Unlock()

	message := fmt.Sprintf("🟢%s is online", client.Name)
	go func(msg string) {
		if err := messageSender.SendTextMessage(msg); err != nil {
			log.Println("Failed to send online notification:", err)
		}
	}(message)
}
