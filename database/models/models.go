package models

import (
	"time"
)

// Client represents a registered client device
type Client struct {
	UUID       string `gorm:"type:uuid;primaryKey" json:"uuid,omitempty"`
	Token      string `gorm:"type:varchar(255);unique;not null" json:"token,omitempty"`
	ClientName string `gorm:"type:varchar(100);not null"`
	CpuName    string `gorm:"type:varchar(100)"`
	CpuCores   uint
	GpuName    string `gorm:"type:varchar(100)"`
	Os         string `gorm:"type:varchar(100)"`
	Memory     int64  `gorm:"type:bigint"`
	IPv4       string `gorm:"type:varchar(100)"`
	IPv6       string `gorm:"type:varchar(100)"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// User represents an authenticated user
type User struct {
	UUID      string `gorm:"type:uuid;primaryKey"`
	Username  string `gorm:"type:varchar(50);unique;not null"`
	Passwd    string `gorm:"type:varchar(255);not null" json:"password,omitempty"` // Hashed password
	SSOID     string `gorm:"type:varchar(100)"  json:"ssoid,omitempty"`            // OAuth provider's user ID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Session manages user sessions
type Session struct {
	UUID      string    `gorm:"type:uuid;primaryKey"`
	UserUUID  string    `gorm:"type:uuid;foreignKey:UserUUID;references:UUID;constraint:OnDelete:CASCADE"`
	Session   string    `gorm:"type:varchar(255);unique;not null"`
	Expires   time.Time `gorm:"not null"`
	CreatedAt time.Time
}

// History logs client metrics over time
type History struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	ClientUUID     string    `gorm:"type:uuid;index;foreignKey:ClientUUID;references:UUID;constraint:OnDelete:CASCADE"`
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
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	Sitename    string `gorm:"type:varchar(100);not null"`
	Description string `gorm:"type:text"`
	OAuthID     string `gorm:"type:varchar(255);not null"`
	OAuthSecret string `gorm:"type:varchar(255);not null"`
	CustomCSS   string `gorm:"type:text"`
	CustomJS    string `gorm:"type:text"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
