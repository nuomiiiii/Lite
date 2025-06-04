package models

import (
	"time"
)

// Client represents a registered client device
type Client struct {
	UUID           string    `json:"uuid,omitempty" gorm:"type:varchar(36);primaryKey"`
	Token          string    `json:"token,omitempty" gorm:"type:varchar(255);unique;not null"`
	Name           string    `json:"name" gorm:"type:varchar(100)"`
	CpuName        string    `json:"cpu_name" gorm:"type:varchar(100)"`
	Virtualization string    `json:"virtualization" gorm:"type:varchar(50)"`
	Arch           string    `json:"arch" gorm:"type:varchar(50)"`
	CpuCores       int       `json:"cpu_cores" gorm:"type:int"`
	OS             string    `json:"os" gorm:"type:varchar(100)"`
	GpuName        string    `json:"gpu_name" gorm:"type:varchar(100)"`
	IPv4           string    `json:"ipv4,omitempty" gorm:"type:varchar(100)"`
	IPv6           string    `json:"ipv6,omitempty" gorm:"type:varchar(100)"`
	Region         string    `json:"region" gorm:"type:varchar(100)"`
	Remark         string    `json:"remark,omitempty" gorm:"type:longtext"`
	PublicRemark   string    `json:"public_remark,omitempty" gorm:"type:longtext"`
	MemTotal       int64     `json:"mem_total" gorm:"type:bigint"`
	SwapTotal      int64     `json:"swap_total" gorm:"type:bigint"`
	DiskTotal      int64     `json:"disk_total" gorm:"type:bigint"`
	Version        string    `json:"version,omitempty" gorm:"type:varchar(100)"`
	Weight         int       `json:"weight" gorm:"type:int"`
	Price          float64   `json:"price"`
	BillingCycle   int       `json:"billing_cycle"`
	ExpiredAt      time.Time `json:"expired_at" gorm:"type:timestamp"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// User represents an authenticated user
type User struct {
	UUID      string    `json:"uuid,omitempty" gorm:"type:varchar(36);primaryKey"`
	Username  string    `json:"username" gorm:"type:varchar(50);unique;not null"`
	Passwd    string    `json:"passwd,omitempty" gorm:"type:varchar(255);not null"` // Hashed password
	SSOType   string    `json:"sso_type" gorm:"type:varchar(20)"`                   // e.g., "github", "google"
	SSOID     string    `json:"sso_id" gorm:"type:varchar(100)"`                    // OAuth provider's user ID
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Session manages user sessions
type Session struct {
	UUID      string    `gorm:"type:varchar(36);foreignKey:UserUUID;references:UUID;constraint:OnDelete:CASCADE"`
	Session   string    `gorm:"type:varchar(255);uniqueIndex:idx_sessions_session;not null"`
	Expires   time.Time `gorm:"not null"`
	CreatedAt time.Time
}

// Record logs client metrics over time
type Record struct {
	Client         string    `json:"client" gorm:"type:varchar(36);index;foreignKey:ClientUUID;references:UUID;constraint:OnDelete:CASCADE"`
	Time           time.Time `json:"time" gorm:"index"`
	Cpu            float32   `json:"cpu" gorm:"type:decimal(5,2)"` // e.g., 75.50%
	Gpu            float32   `json:"gpu" gorm:"type:decimal(5,2)"`
	Ram            int64     `json:"ram" gorm:"type:bigint"`
	RamTotal       int64     `json:"ram_total" gorm:"type:bigint"`
	Swap           int64     `json:"swap" gorm:"type:bigint"`
	SwapTotal      int64     `json:"swap_total" gorm:"type:bigint"`
	Load           float32   `json:"load" gorm:"type:decimal(5,2)"`
	Temp           float32   `json:"temp" gorm:"type:decimal(5,2)"`
	Disk           int64     `json:"disk" gorm:"type:bigint"`
	DiskTotal      int64     `json:"disk_total" gorm:"type:bigint"`
	NetIn          int64     `json:"net_in" gorm:"type:bigint"`
	NetOut         int64     `json:"net_out" gorm:"type:bigint"`
	NetTotalUp     int64     `json:"net_total_up" gorm:"type:bigint"`
	NetTotalDown   int64     `json:"net_total_down" gorm:"type:bigint"`
	Process        int       `json:"process"`
	Connections    int       `json:"connections"`
	ConnectionsUdp int       `json:"connections_udp"`
}

// Config stores site-wide settings
type Config struct {
	ID          uint   `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Sitename    string `json:"sitename" gorm:"type:varchar(100);not null"`
	Description string `json:"description" gorm:"type:text"`
	AllowCors   bool   `json:"allow_cors" gorm:"column:allow_cors;default:false"`
	// GeoIP 配置
	GeoIpEnabled  bool   `json:"geo_ip_enabled" gorm:"default:true"`
	GeoIpProvider string `json:"geo_ip_provider" gorm:"type:varchar(20);default:'mmdb'"` // mmdb, bilibili, ip-api. 暂时只实现了mmdb
	// OAuth 配置
	OAuthClientID     string `json:"o_auth_client_id" gorm:"type:varchar(255);not null"`
	OAuthClientSecret string `json:"o_auth_client_secret" gorm:"type:varchar(255);not null"`
	OAuthEnabled      bool   `json:"o_auth_enabled" gorm:"default:false"`
	// 自定义美化
	CustomHead string `json:"custom_head" gorm:"type:longtext"`
	CustomBody string `json:"custom_body" gorm:"type:longtext"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
