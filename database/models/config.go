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
	OAuthClientID        string `json:"o_auth_client_id" gorm:"type:varchar(255);not null"`
	OAuthClientSecret    string `json:"o_auth_client_secret" gorm:"type:varchar(255);not null"`
	OAuthEnabled         bool   `json:"o_auth_enabled" gorm:"default:false"`
	DisablePasswordLogin bool   `json:"disable_password_login" gorm:"default:false"`
	// 自定义美化
	CustomHead string `json:"custom_head" gorm:"type:longtext"`
	CustomBody string `json:"custom_body" gorm:"type:longtext"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
