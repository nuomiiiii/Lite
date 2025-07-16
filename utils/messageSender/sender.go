package messageSender

import (
	"github.com/komari-monitor/komari/database/auditlog"
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
	var err error
	cfg, err := config.Get()
	if err != nil {
		return err
	}
	if !cfg.NotificationEnabled {
		return nil
	}
	for i := 0; i < 3; i++ {
		err = CurrentProvider.SendTextMessage(message, title)
		if err == nil {
			auditlog.Log("", "", "Message sent: "+title, "info")
			return nil
		}
	}
	auditlog.Log("", "", "Failed to send message after 3 attempts: "+err.Error()+","+title, "error")
	return err
}
