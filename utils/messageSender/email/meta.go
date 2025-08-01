package email

import (
	"github.com/komari-monitor/komari/utils/messageSender/factory"
)

type Addition struct {
	Host     string `json:"host" required:"true"`
	Port     int    `json:"port" required:"true" default:"587"`
	Username string `json:"username"`
	Password string `json:"password"`
	Sender   string `json:"sender"`
	Receiver string `json:"receiver"`
	UseSSL   bool   `json:"use_ssl" default:"false"`
}

func init() {
	factory.RegisterMessageSender(func() factory.IMessageSender {
		return &EmailSender{}
	})
}
