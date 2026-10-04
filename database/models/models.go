package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Client represents a registered client device
type Client struct {
	UUID                   string     `json:"uuid,omitempty" gorm:"type:varchar(36);primaryKey"`
	Token                  string     `json:"token,omitempty" gorm:"type:varchar(255);unique;not null"`
	PreviousToken          string     `json:"-" gorm:"type:varchar(255);index"`
	PreviousTokenExpiresAt *time.Time `json:"-" gorm:"type:timestamp"`
	Name                   string     `json:"name" gorm:"type:varchar(100)"`
	CpuName                string     `json:"cpu_name" gorm:"type:varchar(100)"`
	Virtualization         string     `json:"virtualization" gorm:"type:varchar(50)"`
	Arch                   string     `json:"arch" gorm:"type:varchar(50)"`
	CpuCores               int        `json:"cpu_cores" gorm:"type:int"`
	CpuPhysicalCores       int        `json:"cpu_physical_cores" gorm:"type:int"`
	OS                     string     `json:"os" gorm:"type:varchar(100)"`
	KernelVersion          string     `json:"kernel_version" gorm:"type:varchar(100)"`
	GpuName                string     `json:"gpu_name" gorm:"type:varchar(100)"`
	IPv4                   string     `json:"ipv4,omitempty" gorm:"type:varchar(100)"`
	IPv6                   string     `json:"ipv6,omitempty" gorm:"type:varchar(100)"`
	Region                 string     `json:"region" gorm:"type:varchar(100)"`
	RegionOverride         string     `json:"region_override" gorm:"type:varchar(16);not null;default:''"`
	Remark                 string     `json:"remark,omitempty" gorm:"type:longtext"`
	PublicRemark           string     `json:"public_remark,omitempty" gorm:"type:longtext"`
	MemTotal               int64      `json:"mem_total" gorm:"type:bigint"`
	SwapTotal              int64      `json:"swap_total" gorm:"type:bigint"`
	DiskTotal              int64      `json:"disk_total" gorm:"type:bigint"`
	Version                string     `json:"version,omitempty" gorm:"type:varchar(100)"`
	Weight                 int        `json:"weight" gorm:"type:int"`
	Price                  float64    `json:"price"`
	BillingCycle           int        `json:"billing_cycle"`
	AutoRenewal            bool       `json:"auto_renewal" gorm:"default:false"` // 是否自动续费
	Currency               string     `json:"currency" gorm:"type:varchar(20);default:'$'"`
	ExpiredAt              *time.Time `json:"expired_at" gorm:"type:timestamp"`
	ExpiryTimezone         string     `json:"expiry_timezone" gorm:"type:varchar(64);not null;default:'Asia/Shanghai'"`
	Group                  string     `json:"group" gorm:"type:varchar(100)"`
	Tags                   string     `json:"tags" gorm:"type:text"` // split by ';'
	Bandwidth              string     `json:"bandwidth" gorm:"type:varchar(64);not null;default:''"`
	Hidden                 bool       `json:"hidden" gorm:"default:false"`
	RemoteProtocol         int        `json:"remote_protocol" gorm:"default:0"`
	RemoteControlEnabled   bool       `json:"remote_control_enabled" gorm:"default:false"`
	RemoteControlProtected bool       `json:"-" gorm:"column:remote_control_protected;default:false"`
	MCPFull                bool       `json:"mcp_full" gorm:"column:mcp_full;default:false"`
	MCPFullVersion         int        `json:"mcp_full_version" gorm:"column:mcp_full_version;default:0"`
	TrafficLimit           int64      `json:"traffic_limit" gorm:"type:bigint"`
	TrafficLimitType       string     `json:"traffic_limit_type" gorm:"type:varchar(10);default:'sum'"` // 流量阈值类型：sum max min up down
	TrafficResetDay        *int       `json:"traffic_reset_day,omitempty" gorm:"type:int"`              // nil: follow agent; 0: disabled; 1-31: monthly reset day
	TrafficResetTime       string     `json:"traffic_reset_time" gorm:"type:varchar(8);not null;default:'00:00:00'"`
	TrafficResetTimezone   string     `json:"traffic_reset_timezone" gorm:"type:varchar(64);not null;default:'Asia/Shanghai'"`
	TrafficResetAllowance  int64      `json:"traffic_reset_allowance" gorm:"type:bigint;not null;default:0"`
	TrafficResetCycle      string     `json:"traffic_reset_cycle,omitempty" gorm:"type:varchar(64);not null;default:''"`
	EffectiveTrafficLimit  int64      `json:"effective_traffic_limit" gorm:"-"`
	EffectiveTrafficType   string     `json:"effective_traffic_type" gorm:"-"`
	DeploymentStatus       string     `json:"deployment_status" gorm:"-"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// ClientDeploymentProfile stores private per-node installation preferences.
// Config is only exposed through the dedicated administrator API.
type ClientDeploymentProfile struct {
	Client            string     `json:"-" gorm:"type:varchar(36);primaryKey"`
	Config            string     `json:"-" gorm:"type:text;not null"`
	Revision          uint64     `json:"-" gorm:"not null;default:0"`
	DeliveryStatus    string     `json:"-" gorm:"type:varchar(16);not null;default:''"`
	DeliveryError     string     `json:"-" gorm:"type:varchar(512);not null;default:''"`
	SavedAt           *time.Time `json:"-"`
	DeliveryUpdatedAt *time.Time `json:"-"`
	SentAt            *time.Time `json:"-"`
	FinishedAt        *time.Time `json:"-"`
	CreatedAt         time.Time  `json:"-"`
	UpdatedAt         time.Time  `json:"-"`
}

// User represents an authenticated user
type User struct {
	UUID             string    `json:"uuid,omitempty" gorm:"type:varchar(36);primaryKey"`
	Username         string    `json:"username" gorm:"type:varchar(50);unique;not null"`
	Passwd           string    `json:"passwd,omitempty" gorm:"type:varchar(255);not null"` // Argon2id or legacy SHA-256
	SSOType          string    `json:"sso_type" gorm:"type:varchar(20)"`                   // e.g., "github", "google"
	SSOID            string    `json:"sso_id" gorm:"type:varchar(100)"`                    // OAuth provider's user ID
	TwoFactor        string    `json:"-" gorm:"type:varchar(512)"`                         // Encrypted TOTP secret
	TwoFactorCounter int64     `json:"-" gorm:"not null;default:0"`                        // Last accepted TOTP counter
	Language         string    `json:"language,omitempty" gorm:"type:varchar(32);not null;default:''"`
	Color            string    `json:"color,omitempty" gorm:"type:varchar(16);not null;default:''"`
	AvatarVersion    string    `json:"avatar_version,omitempty" gorm:"type:varchar(64);not null;default:''"`
	Sessions         []Session `json:"sessions,omitempty" gorm:"foreignKey:UUID;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// PasskeyCredential stores a WebAuthn authenticator for one account.
type PasskeyCredential struct {
	ID                string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	UserUUID          string     `json:"user_uuid" gorm:"type:varchar(36);index;not null"`
	RPID              string     `json:"rp_id" gorm:"type:varchar(255);uniqueIndex:ux_passkey_rp_credential;not null"`
	CredentialID      string     `json:"credential_id" gorm:"type:varchar(512);uniqueIndex:ux_passkey_rp_credential;not null"`
	Name              string     `json:"name" gorm:"type:varchar(100);not null"`
	CredentialData    []byte     `json:"-" gorm:"type:blob;not null"`
	AAGUID            string     `json:"aaguid,omitempty" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt         time.Time  `json:"created_at"`
	LastUsedAt        *time.Time `json:"last_used_at"`
	CreationUserAgent string     `json:"creation_user_agent" gorm:"type:text"`
}

// Session manages user sessions
type Session struct {
	UUID            string    `json:"uuid" gorm:"type:varchar(36)"`
	Session         string    `json:"session" gorm:"type:varchar(255);primaryKey;uniqueIndex:idx_sessions_session;not null"`
	UserAgent       string    `json:"user_agent" gorm:"type:text"`
	Ip              string    `json:"ip" gorm:"type:varchar(100)"`
	LoginMethod     string    `json:"login_method" gorm:"type:varchar(50)"`
	LatestOnline    time.Time `json:"latest_online" gorm:"type:timestamp"`
	LatestUserAgent string    `json:"latest_user_agent" gorm:"type:text"`
	LatestIp        string    `json:"latest_ip" gorm:"type:varchar(100)"`
	Expires         time.Time `json:"expires" gorm:"not null"`
	CreatedAt       time.Time `json:"created_at"`
}

// Record logs client metrics over time
type Record struct {
	Client         string    `json:"client" gorm:"type:varchar(36);index"`
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
	TrafficUp      int64     `json:"traffic_up" gorm:"type:bigint"`
	TrafficDown    int64     `json:"traffic_down" gorm:"type:bigint"`
	TrafficUpSet   bool      `json:"-" gorm:"-"`
	TrafficDownSet bool      `json:"-" gorm:"-"`
	Process        int       `json:"process"`
	Connections    int       `json:"connections"`
	ConnectionsUdp int       `json:"connections_udp"`
	//Uptime         int64     `json:"uptime" gorm:"type:bigint"`
}

// GPURecord logs individual GPU metrics over time
type GPURecord struct {
	Client      string    `json:"client" gorm:"type:varchar(36);index"` // 客户端UUID
	Time        time.Time `json:"time" gorm:"index"`                    // 记录时间
	DeviceIndex int       `json:"device_index" gorm:"index"`            // GPU设备索引 (0,1,2...)
	DeviceName  string    `json:"device_name" gorm:"type:varchar(100)"` // GPU型号
	MemTotal    int64     `json:"mem_total" gorm:"type:bigint"`         // 显存总量(字节)
	MemUsed     int64     `json:"mem_used" gorm:"type:bigint"`          // 显存使用(字节)
	Utilization float32   `json:"utilization" gorm:"type:decimal(5,2)"` // GPU使用率(%)
	Temperature int       `json:"temperature"`                          // GPU温度(°C)
}

// StringArray represents a slice of strings stored as JSON in the database
// StringArray 存储为 JSON 的字符串切片类型
type StringArray []string

func (sa *StringArray) Scan(value interface{}) error {
	var bytes []byte
	switch v := value.(type) {
	case nil:
		*sa = StringArray{}
		return nil
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("failed to scan StringArray: unsupported value type %T", value)
	}
	if len(bytes) == 0 {
		*sa = StringArray{}
		return nil
	}
	return json.Unmarshal(bytes, sa)
}

func (sa StringArray) Value() (driver.Value, error) {
	return json.Marshal(sa)
}

type MCPLease struct {
	ID                     string     `json:"id" gorm:"type:varchar(64);primaryKey"`
	OwnerUserUUID          string     `json:"owner_user_uuid" gorm:"type:varchar(36);index;not null"`
	OwnerLoginSessionHash  string     `json:"-" gorm:"type:varchar(64);index;not null"`
	OAuthClientID          string     `json:"oauth_client_id" gorm:"type:varchar(128);index;not null"`
	AuthorizationRequestID string     `json:"authorization_request_id" gorm:"type:varchar(64)"`
	TokenFamilyID          string     `json:"token_family_id" gorm:"type:varchar(64);index;not null"`
	TargetUUIDs            string     `json:"target_uuids" gorm:"type:text;not null"`
	Mode                   string     `json:"mode" gorm:"type:varchar(16);not null;default:'full'"`
	Note                   string     `json:"note" gorm:"type:varchar(200)"`
	MaxConcurrency         int        `json:"max_concurrency" gorm:"not null;default:4"`
	Status                 string     `json:"status" gorm:"type:varchar(24);index;not null"`
	PolicyVersion          int        `json:"policy_version" gorm:"not null;default:1"`
	LongTerm               bool       `json:"long_term" gorm:"not null;default:false"`
	RevocationReason       string     `json:"revocation_reason" gorm:"type:varchar(64)"`
	CreatedAt              time.Time  `json:"created_at"`
	ExpiresAt              time.Time  `json:"expires_at" gorm:"index"`
	RevokedAt              *time.Time `json:"revoked_at"`
}

func (MCPLease) TableName() string { return "mcp_leases" }

type MCPToken struct {
	Hash                string    `json:"-" gorm:"type:varchar(64);primaryKey"`
	Kind                string    `json:"kind" gorm:"type:varchar(16);index;not null"`
	FamilyID            string    `json:"family_id" gorm:"type:varchar(64);index;not null"`
	LeaseID             string    `json:"lease_id" gorm:"type:varchar(64);index"`
	ClientID            string    `json:"client_id" gorm:"type:varchar(128);index;not null"`
	RedirectURI         string    `json:"redirect_uri" gorm:"type:varchar(512)"`
	CodeChallenge       string    `json:"-" gorm:"type:varchar(128)"`
	CodeChallengeMethod string    `json:"-" gorm:"type:varchar(16)"`
	Resource            string    `json:"resource" gorm:"type:varchar(512)"`
	Used                bool      `json:"used" gorm:"default:false"`
	ExpiresAt           time.Time `json:"expires_at" gorm:"index"`
	CreatedAt           time.Time `json:"created_at"`
}

func (MCPToken) TableName() string { return "mcp_tokens" }

type MCPClient struct {
	ClientID                string    `json:"client_id" gorm:"type:varchar(128);primaryKey"`
	ClientName              string    `json:"client_name" gorm:"type:varchar(120)"`
	RedirectURIs            string    `json:"redirect_uris" gorm:"type:text"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method" gorm:"type:varchar(64);not null;default:'none'"`
	CreatedAt               time.Time `json:"created_at"`
}

func (MCPClient) TableName() string { return "mcp_clients" }

type MCPAuthorizationRequest struct {
	ID                  string    `json:"id" gorm:"type:varchar(64);primaryKey"`
	ClientID            string    `json:"client_id" gorm:"type:varchar(128);index;not null"`
	RedirectURI         string    `json:"redirect_uri" gorm:"type:varchar(512);not null"`
	State               string    `json:"state" gorm:"type:varchar(256)"`
	CodeChallenge       string    `json:"-" gorm:"type:varchar(128);not null"`
	CodeChallengeMethod string    `json:"-" gorm:"type:varchar(16);not null"`
	Resource            string    `json:"resource" gorm:"type:varchar(512)"`
	Scope               string    `json:"scope" gorm:"type:varchar(64)"`
	OwnerUserUUID       string    `json:"owner_user_uuid" gorm:"type:varchar(36);index"`
	Status              string    `json:"status" gorm:"type:varchar(24);index;not null"`
	CreatedAt           time.Time `json:"created_at"`
	ExpiresAt           time.Time `json:"expires_at"`
}

func (MCPAuthorizationRequest) TableName() string { return "mcp_authorization_requests" }

type MCPOperation struct {
	ID              string     `json:"id" gorm:"type:varchar(64);primaryKey"`
	LeaseID         string     `json:"lease_id" gorm:"type:varchar(64);index;not null"`
	AgentUUID       string     `json:"agent_uuid" gorm:"type:varchar(36);index;not null"`
	ToolName        string     `json:"tool_name" gorm:"type:varchar(64);not null"`
	ClientRequestID string     `json:"client_request_id" gorm:"type:varchar(128)"`
	IdempotencyKey  string     `json:"idempotency_key" gorm:"type:varchar(128);index"`
	RequestDigest   string     `json:"request_digest" gorm:"type:varchar(64)"`
	State           string     `json:"state" gorm:"type:varchar(24);index;not null"`
	ExitCode        int        `json:"exit_code"`
	Output          string     `json:"-" gorm:"type:longtext"`
	Truncated       bool       `json:"truncated" gorm:"default:false"`
	Deadline        time.Time  `json:"deadline"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (MCPOperation) TableName() string { return "mcp_operations" }
