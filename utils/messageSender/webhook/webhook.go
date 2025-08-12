package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/komari-monitor/komari/utils/messageSender/factory"
)

type WebhookSender struct {
	Addition
}

func (w *WebhookSender) GetName() string {
	return "webhook"
}

func (w *WebhookSender) GetConfiguration() factory.Configuration {
	return &w.Addition
}

func (w *WebhookSender) Init() error {
	return nil
}

func (w *WebhookSender) Destroy() error {
	return nil
}

func (w *WebhookSender) SendTextMessage(message, title string) error {
	if w.Addition.URL == "" {
		return fmt.Errorf("webhook URL is not configured")
	}

	method := strings.ToUpper(w.Addition.Method)
	if method == "" {
		method = "GET" // 默认使用 GET
	}

	client := &http.Client{
		Timeout: 30 * time.Second, // 固定30秒超时
	}

	var req *http.Request
	var err error

	switch method {
	case "POST":
		req, err = w.createPOSTRequest(message, title)
	case "GET":
		req, err = w.createGETRequest(message, title)
	default:
		return fmt.Errorf("unsupported HTTP method: %s", method)
	}

	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	// 解析并设置自定义头部
	if w.Addition.Headers != "" {
		var headers map[string]string
		if err := json.Unmarshal([]byte(w.Addition.Headers), &headers); err == nil {
			for key, value := range headers {
				req.Header.Set(key, value)
			}
		}
	}

	// 设置基本认证
	if w.Addition.Username != "" && w.Addition.Password != "" {
		req.SetBasicAuth(w.Addition.Username, w.Addition.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send webhook request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (w *WebhookSender) createPOSTRequest(message, title string) (*http.Request, error) {
	// 替换模板中的占位符
	body := w.replaceTemplate(w.Addition.Body, message, title)

	contentType := w.Addition.ContentType
	if contentType == "" {
		contentType = "application/json"
	}

	req, err := http.NewRequest("POST", w.Addition.URL, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", contentType)
	return req, nil
}

func (w *WebhookSender) createGETRequest(message, title string) (*http.Request, error) {
	URL := w.replaceTemplate(w.Addition.URL, message, title)
	u, err := url.Parse(URL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %v", err)
	}

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	return req, nil
}

// replaceTemplate 替换模板中的 {{message}} 和 {{title}} 占位符
func (w *WebhookSender) replaceTemplate(template, message, title string) string {
	result := template
	result = strings.ReplaceAll(result, "{{message}}", message)
	result = strings.ReplaceAll(result, "{{title}}", title)
	return result
}
