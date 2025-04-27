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
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// OAuthConfig stores OAuth provider configurations
type OAuthConfig struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	Provider     string `gorm:"type:varchar(20);not null;unique"` // e.g., "github"
	ClientID     string `gorm:"type:varchar(255);not null"`
	ClientSecret string `gorm:"type:varchar(255);not null"`
	RedirectURI  string `gorm:"type:varchar(255);not null"`
	Enabled      bool   `gorm:"default:false"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Custom stores custom configurations
type Custom struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	CustomCSS string `gorm:"type:text"`
	CustomJS  string `gorm:"type:text"`
	SiteName  string `gorm:"type:varchar(100);not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
