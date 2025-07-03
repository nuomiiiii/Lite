package messageSender

import (
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/database/logOperation"
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
	err := CurrentProvider.SendTextMessage(message, title)
	if err != nil {
		logOperation.Log("", "", "Failed to send message: "+err.Error(), "error")
	} else {
		logOperation.Log("", "", "Message sent: "+title, "info")
	}
	return err
}
