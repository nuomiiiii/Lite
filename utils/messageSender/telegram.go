package messageSender

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/komari-monitor/komari/database/config"
)

// TelegramMessageSender implements the MessageSender interface for Telegram.
type TelegramMessageSender struct{}

// SendTextMessage sends a text message via Telegram API.
// The title is prepended to the message.
func (t *TelegramMessageSender) SendTextMessage(message, title string) error {
	fullMessage := message
	if title != "" {
		fullMessage = fmt.Sprintf("<b>%s</b>\n%s", title, message)
	}

	var err error
	conf, err := config.Get()
	if err != nil {
		return err
	}

	if fullMessage == "" {
		return errors.New("message is empty")
	}

	endpoint := conf.TelegramEndpoint + conf.TelegramBotToken + "/sendMessage"

	data := url.Values{}
	data.Set("chat_id", conf.TelegramChatID)
	data.Set("text", fullMessage)
	data.Set("parse_mode", "HTML")

	resp, err := http.PostForm(endpoint, data)
	if err != nil {
		return fmt.Errorf("failed to send message: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API returned non-OK status: %d", resp.StatusCode)
	}

	var result struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if !result.Ok {
		return fmt.Errorf("telegram API error: %s", result.Description)
	}

	return nil
}
