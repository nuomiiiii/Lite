package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/komari-monitor/komari/database/config"
)

func SendMessage(message, msgType string) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		conf, err := config.Get()
		if err != nil {
			lastErr = err
			continue
		}

		if !conf.TelegramEnabled || message == "" {
			return errors.New("telegram is disabled or message is empty")
		}

		endpoint := conf.TelegramEndpoint + conf.TelegramBotToken + "/sendMessage"

		data := url.Values{}
		data.Set("chat_id", conf.TelegramChatID)
		data.Set("text", message)
		if msgType != "text" {
			data.Set("parse_mode", msgType)
		}
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

// SendTextMessage sends a text message via Telegram API
func SendTextMessage(message string) error {
	return SendMessage(message, "text")
}

// SendMarkdownMessage sends a message formatted in Markdown via Telegram API
func SendMarkdownMessage(message string) error {
	return SendMessage(message, "MarkdownV2")
}
func SendHTMLMessage(message string) error {
	return SendMessage(message, "HTML")
}
