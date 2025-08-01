package email

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strconv"

	"github.com/komari-monitor/komari/utils/messageSender/factory"
)

type EmailSender struct {
	Addition
}

func (e *EmailSender) GetName() string {
	return "email"
}

func (e *EmailSender) GetConfiguration() factory.Configuration {
	return &e.Addition
}

func (e *EmailSender) Init() error {
	return nil
}

func (e *EmailSender) Destroy() error {
	return nil
}

func (e *EmailSender) SendTextMessage(message, title string) error {

	if e.Addition.Host == "" || e.Addition.Sender == "" || e.Addition.Username == "" || e.Addition.Password == "" || e.Addition.Receiver == "" {
		return fmt.Errorf("email sending is not fully configured")
	}

	auth := smtp.PlainAuth(
		"",
		e.Addition.Username,
		e.Addition.Password,
		e.Addition.Host,
	)

	msg := []byte("To: " + e.Addition.Receiver + "\r\n" +
		"From: " + e.Addition.Sender + "\r\n" +
		"Subject: " + title + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		message)

	addr := e.Addition.Host + ":" + strconv.Itoa(e.Addition.Port)

	if e.Addition.UseSSL {
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("failed to dial SMTP server: %w", err)
		}
		defer c.Close()

		if err = c.StartTLS(&tls.Config{ServerName: e.Addition.Host}); err != nil {
			return fmt.Errorf("failed to start TLS: %w", err)
		}

		// Authenticate
		if err = c.Auth(auth); err != nil {
			return fmt.Errorf("failed to authenticate: %w", err)
		}

		// Set the sender and recipient
		if err = c.Mail(e.Addition.Sender); err != nil {
			return fmt.Errorf("failed to set sender: %w", err)
		}
		if err = c.Rcpt(e.Addition.Receiver); err != nil {
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
			e.Addition.Sender,
			[]string{e.Addition.Receiver},
			msg,
		)
	}
}

// 确保实现了 IMessageSender 接口
var _ factory.IMessageSender = (*EmailSender)(nil)
