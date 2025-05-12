package models

import (
	"time"
)

// Client represents a registered client device
type Client struct {
	UUID      string    `json:"uuid,omitempty" gorm:"type:uuid;primaryKey"`
	Token     string    `json:"token,omitempty" gorm:"type:varchar(255);unique;not null"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// User represents an authenticated user
type User struct {
	UUID      string    `json:"uuid,omitempty" gorm:"type:uuid;primaryKey"`
	Username  string    `json:"username" gorm:"type:varchar(50);unique;not null"`
	Passwd    string    `json:"passwd,omitempty" gorm:"type:varchar(255);not null"` // Hashed password
	SSOType   string    `json:"sso_type" gorm:"type:varchar(20)"`                   // e.g., "github", "google"
	SSOID     string    `json:"sso_id" gorm:"type:varchar(100)"`                    // OAuth provider's user ID
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Session manages user sessions
type Session struct {
	UUID      string    `gorm:"type:uuid;foreignKey:UserUUID;references:UUID;constraint:OnDelete:CASCADE"`
	Session   string    `gorm:"type:varchar(255);unique;not null"`
	Expires   time.Time `gorm:"not null"`
	CreatedAt time.Time
}

// Record logs client metrics over time
type Record struct {
	Client         string    `gorm:"type:uuid;index;foreignKey:ClientUUID;references:UUID;constraint:OnDelete:CASCADE"`
	Time           time.Time `gorm:"index;default:CURRENT_TIMESTAMP"`
	CPU            float32   `gorm:"type:decimal(5,2)"` // e.g., 75.50%
	GPU            float32   `gorm:"type:decimal(5,2)"`
	RAM            int64     `gorm:"type:bigint"`
	RAMTotal       int64     `gorm:"type:bigint"`
	SWAP           int64     `gorm:"type:bigint"`
	SWAPTotal      int64     `gorm:"type:bigint"`
	LOAD           float32   `gorm:"type:decimal(5,2)"`
	TEMP           float32   `gorm:"type:decimal(5,2)"`
	DISK           int64     `gorm:"type:bigint"`
	DISKTotal      int64     `gorm:"type:bigint"`
	NETIn          int64     `gorm:"type:bigint"`
	NETOut         int64     `gorm:"type:bigint"`
	NETTotalUp     int64     `gorm:"type:bigint"`
	NETTotalDown   int64     `gorm:"type:bigint"`
	PROCESS        int
	Connections    int
	ConnectionsUDP int
}

// Config stores site-wide settings
type Config struct {
	ID          uint   `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Sitename    string `json:"sitename" gorm:"type:varchar(100);not null"`
	Description string `json:"description" gorm:"type:text"`
	AllowCros   bool   `json:"allow_cros" gorm:"default:false"`
	// GeoIP 配置
	GeoIpEnabled  bool   `json:"geoip_enable" gorm:"default:true"`
	GeoIpProvider string `json:"geoip_provider" gorm:"type:varchar(20);default:'mmdb'"` // mmdb, bilibili, ip-api. 暂时只实现了mmdb
	// OAuth 配置
	OAuthClientID     string `json:"oauth_id" gorm:"type:varchar(255);not null"`
	OAuthClientSecret string `json:"oauth_secret" gorm:"type:varchar(255);not null"`
	OAuthEnabled      bool   `json:"oauth_enable" gorm:"default:false"`
	// 自定义美化
	CustomHead string `json:"custom_head" gorm:"type:longtext"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
