package messageSender

import (
	"github.com/komari-monitor/komari/database/config"
)

var CurrentProvider MessageSender

func init() {
	CurrentProvider = &EmptyProvider{}
}

func Initialize() {
	cfg, err := config.Get()
	if err != nil {
		CurrentProvider = &EmptyProvider{}
		return
	}

	switch cfg.NotificationMethod {
	case "telegram":
		CurrentProvider = &TelegramMessageSender{}
	case "email":
		CurrentProvider = &EmailMessageSender{}
	case "none", "":
		CurrentProvider = &EmptyProvider{}
	default:
		CurrentProvider = &EmptyProvider{}
	}
}

type MessageSender interface {
	SendTextMessage(message, title string) error
}

func SendTextMessage(message string, title string) error {
	return CurrentProvider.SendTextMessage(message, title)
}
