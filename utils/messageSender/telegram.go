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

	var lastErr error
	for i := 0; i < 3; i++ { // Retry mechanism
		conf, err := config.Get()
		if err != nil {
			lastErr = err
			continue
		}

		if fullMessage == "" {
			return errors.New("telegram is disabled or message is empty") // Assuming disabled if message is empty
		}

		endpoint := conf.TelegramEndpoint + conf.TelegramBotToken + "/sendMessage"

		data := url.Values{}
		data.Set("chat_id", conf.TelegramChatID)
		data.Set("text", fullMessage)
		data.Set("parse_mode", "HTML") // Use HTML for basic formatting if title is present

		resp, err := http.PostForm(endpoint, data)
		if err != nil {
			lastErr = fmt.Errorf("failed to send message: %v", err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("telegram API returned non-OK status: %d", resp.StatusCode)
			continue
		}

		var result struct {
			Ok          bool   `json:"ok"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			lastErr = err
			continue
		}

		if !result.Ok {
			lastErr = fmt.Errorf("telegram API error: %s", result.Description)
			continue
		}

		return nil
	}
	return lastErr
}