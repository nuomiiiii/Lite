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
	"github.com/komari-monitor/komari/utils/telegram"
)

var (
	lastNotified       = make(map[string]time.Time)
	lastOnlineNotified = make(map[string]time.Time)
	pendingOffline     = make(map[string]time.Time)

	mu sync.Mutex
)

// OfflineNotification sends an offline notification for the client if enabled and not sent in cooldown period
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
		Client:      clientID,
		Enable:      true,
		Cooldown:    1800,
		GracePeriod: 300,
	}

	db := dbcore.GetDBInstance()
	db.Model(&models.OfflineNotification{}).Where("client = ?", clientID).FirstOrCreate(&noti_conf)

	cooldownDuration := time.Duration(noti_conf.Cooldown) * time.Second
	gracePeriod := time.Duration(noti_conf.GracePeriod) * time.Second
	if cooldownDuration <= 0 {
		cooldownDuration = 30 * time.Minute // default cooldown if not set
	}
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
		// check cooldown
		if last, ok := lastNotified[clientID]; ok && time.Since(last) < cooldownDuration {
			delete(pendingOffline, clientID)
			return
		}
		// record notification time and clear pending
		lastNotified[clientID] = time.Now()
		delete(pendingOffline, clientID)

		message := fmt.Sprintf("🔴<code>%s</code> is offline", client.Name)
		go func(msg string) {
			if err := telegram.SendHTMLMessage(msg); err != nil {
				log.Println("Failed to send offline notification:", err)
			}
		}(message)
	}(now)
}

// OnlineNotification sends an online notification for the client if enabled and not sent in cooldown period
func OnlineNotification(clientID string) {
	client, err := clients.GetClientByUUID(clientID)
	if err != nil {
		return
	}
	conf, _ := config.Get()
	if !conf.NotificationEnabled {
		return
	}

	noti_conf := models.OfflineNotification{
		Client:      clientID,
		Enable:      true,
		Cooldown:    1800,
		GracePeriod: 300,
	}

	db := dbcore.GetDBInstance()
	db.Model(&models.OfflineNotification{}).Where("client = ?", clientID).FirstOrCreate(&noti_conf)

	cooldownDuration := time.Duration(noti_conf.Cooldown) * time.Second
	gracePeriod := time.Duration(noti_conf.GracePeriod) * time.Second
	if cooldownDuration <= 0 {
		cooldownDuration = 30 * time.Minute // default cooldown if not set
	}
	if gracePeriod <= 0 {
		gracePeriod = 5 * time.Minute // default grace period if not set
	}
	// clear any pending offline debounce
	mu.Lock()
	delete(pendingOffline, clientID)
	mu.Unlock()

	now := time.Now()

	mu.Lock()
	defer mu.Unlock()

	// if never went offline before, skip first online notification
	if _, wasOffline := lastNotified[clientID]; !wasOffline {
		return
	}
	// cooldown for online notifications
	if last, exists := lastOnlineNotified[clientID]; exists && now.Sub(last) < cooldownDuration {
		return
	}
	// send online notification
	message := fmt.Sprintf("🟢<code>%s</code> is online", client.Name)
	go func(msg string) {
		if err := telegram.SendHTMLMessage(msg); err != nil {
			log.Println("Failed to send online notification:", err)
		}
	}(message)

	lastOnlineNotified[clientID] = now
	delete(lastNotified, clientID)
}
