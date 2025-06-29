package messageSender

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strconv"

	"github.com/komari-monitor/komari/database/config"
)

// EmailMessageSender implements the MessageSender interface for Email.
type EmailMessageSender struct{}

// SendTextMessage sends a text message via Email (SMTP).
// The title is used as the email subject.
func (e *EmailMessageSender) SendTextMessage(message, title string) error {
	conf, err := config.Get()
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	if conf.EmailHost == "" || conf.EmailSender == "" || conf.EmailUsername == "" || conf.EmailPassword == "" || conf.EmailReceiver == "" {
		return fmt.Errorf("email sending is not fully configured")
	}

	auth := smtp.PlainAuth(
		"",
		conf.EmailUsername,
		conf.EmailPassword,
		conf.EmailHost,
	)

	msg := []byte("To: " + conf.EmailReceiver + "\r\n" +
		"From: " + conf.EmailSender + "\r\n" +
		"Subject: " + title + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		message)

	addr := conf.EmailHost + ":" + strconv.Itoa(conf.EmailPort)

	if conf.EmailUseSSL {
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("failed to dial SMTP server: %w", err)
		}
		defer c.Close()

		if err = c.StartTLS(&tls.Config{ServerName: conf.EmailHost}); err != nil {
			return fmt.Errorf("failed to start TLS: %w", err)
		}

		// Authenticate
		if err = c.Auth(auth); err != nil {
			return fmt.Errorf("failed to authenticate: %w", err)
		}

		// Set the sender and recipient
		if err = c.Mail(conf.EmailSender); err != nil {
			return fmt.Errorf("failed to set sender: %w", err)
		}
		if err = c.Rcpt(conf.EmailReceiver); err != nil {
			return fmt.Errorf("failed to set recipient: %w", err)
		}

		w, err := c.Data()
		if err != nil {
			return fmt.Errorf("failed to get data writer: %w", err)
		}
		_, err = w.Write(msg)
		if err != nil {
			return fmt.Errorf("failed to write message: %w", err)
		}
		err = w.Close()
		if err != nil {
			return fmt.Errorf("failed to close data writer: %w", err)
		}

		return c.Quit()
	} else {
		// Send without SSL/TLS (less secure)
		return smtp.SendMail(
			addr,
			auth,
			conf.EmailSender,
			[]string{conf.EmailReceiver},
			msg,
		)
	}
}
