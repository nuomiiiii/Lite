package telegram

type Addition struct {
	BotToken string `json:"bot_token" required:"true"`
	ChatID   string `json:"chat_id" required:"true"`
	Endpoint string `json:"endpoint" required:"true" default:"https://api.telegram.org/bot"`
}
