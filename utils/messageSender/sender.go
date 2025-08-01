package messageSender

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/komari-monitor/komari/database"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils/messageSender/factory"
)

var (
	currentProvider factory.IMessageSender
	mu              = sync.Mutex{}
	once            = sync.Once{}
)

func CurrentProvider() factory.IMessageSender {
	mu.Lock()
	defer mu.Unlock()
	return currentProvider
}

func Initialize() {
	go func() {
		once.Do(func() {
			all := factory.GetAllMessageSenders()
			for _, provider := range all {
				if _, err := database.GetMessageSenderConfigByName(provider.GetName()); err == nil {
					continue
				}
				// 如果数据库中没有该提供者的配置，则保存默认配置
				config := provider.GetConfiguration()
				configBytes, err := json.Marshal(config)
				if err != nil {
					log.Printf("Failed to marshal config for provider %s: %v", provider.GetName(), err)
					return
				}
				if err := database.SaveMessageSenderConfig(&models.MessageSenderProvider{
					Name:     provider.GetName(),
					Addition: string(configBytes),
				}); err != nil {
					log.Printf("Failed to save default config for provider %s: %v", provider.GetName(), err)
					return
				}
			}
		})
	}()
	cfg, _ := config.Get()

	if cfg.NotificationMethod == "" || cfg.NotificationMethod == "none" {
		LoadProvider("empty", "{}")
		return
	}

	// 尝试从数据库加载配置
	senderConfig, err := database.GetMessageSenderConfigByName(cfg.NotificationMethod)
	if err != nil {
		// 如果没有找到配置，使用empty provider
		LoadProvider("empty", "{}")
		return
	}
	LoadProvider(cfg.NotificationMethod, senderConfig.Addition)
}

func SendTextMessage(message string, title string) error {
	if CurrentProvider() == nil {
		return fmt.Errorf("message sender provider is not initialized")
	}
	var err error
	cfg, err := config.Get()
	if err != nil {
		return err
	}
	if !cfg.NotificationEnabled {
		return nil
	}
	for i := 0; i < 3; i++ {
		err = CurrentProvider().SendTextMessage(message, title)
		if err == nil {
			auditlog.Log("", "", "Message sent: "+title, "info")
			return nil
		}
	}
	auditlog.Log("", "", "Failed to send message after 3 attempts: "+err.Error()+","+title, "error")
	return err
}
