package models

import "time"

type Config struct {
	ID          uint   `json:"id,omitempty" gorm:"primaryKey;autoIncrement"` // 1
	Sitename    string `json:"sitename" gorm:"type:varchar(100);not null"`
	Description string `json:"description" gorm:"type:text"`
	AllowCors   bool   `json:"allow_cors" gorm:"column:allow_cors;default:false"`
	// GeoIP 配置
	GeoIpEnabled  bool   `json:"geo_ip_enabled" gorm:"default:true"`
	GeoIpProvider string `json:"geo_ip_provider" gorm:"type:varchar(20);default:'mmdb'"` // mmdb, bilibili, ip-api. 暂时只实现了mmdb
	// OAuth 配置
	OAuthClientID        string `json:"o_auth_client_id" gorm:"type:varchar(255)"`
	OAuthClientSecret    string `json:"o_auth_client_secret" gorm:"type:varchar(255)"`
	OAuthEnabled         bool   `json:"o_auth_enabled" gorm:"default:false"`
	DisablePasswordLogin bool   `json:"disable_password_login" gorm:"default:false"`
	// 自定义美化
	CustomHead string `json:"custom_head" gorm:"type:longtext"`
	CustomBody string `json:"custom_body" gorm:"type:longtext"`
	// Telegram Bot 配置
	TelegramEnabled  bool   `json:"telegram_enabled" gorm:"default:false"`
	TelegramEndpoint string `json:"telegram_endpoint" gorm:"type:varchar(255);default:'https://api.telegram.org/bot'"`
	TelegramBotToken string `json:"telegram_bot_token" gorm:"type:varchar(255)"`
	TelegramChatID   string `json:"telegram_chat_id" gorm:"type:varchar(255)"`
	// 通知
	NotificationEnabled bool `json:"otification_enabled" gorm:"default:false"` // 通知总开关
	CreatedAt           time.Time
	UpdatedAt           time.Time
}
