package messageSender

import (
	"errors"

	"github.com/komari-monitor/komari/database/config"
)

func SendTextMessage(message string) error {
	cfg, _ := config.Get()
	if !cfg.NotificationEnabled {
		return nil
	}
	switch cfg.NotificationMethod {
	case "none":
		return nil
	case "telegram":
		return TelegramSendTextMessage(message)
	default:
		return errors.New("unsupported notification method: " + cfg.NotificationMethod)
	}
}
