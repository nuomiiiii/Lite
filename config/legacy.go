package config

import "time"

type Legacy struct {
	ID                         uint    `json:"id,omitempty"`
	Sitename                   string  `json:"sitename" default:"Komari"`
	Description                string  `json:"description" default:"A simple server monitor tool."`
	AllowCors                  bool    `json:"allow_cors" default:"false"`
	Theme                      string  `json:"theme" default:"default"`
	PrivateSite                bool    `json:"private_site" default:"false"`
	ApiKey                     string  `json:"api_key" default:""`
	AutoDiscoveryKey           string  `json:"auto_discovery_key" default:""`
	ScriptDomain               string  `json:"script_domain" default:""`
	SendIpAddrToGuest          bool    `json:"send_ip_addr_to_guest" default:"false"`
	EulaAccepted               bool    `json:"eula_accepted" default:"false"`
	BaseScriptsURLKey          string  `json:"base_scripts_url" default:""`
	GeoIpEnabled               bool    `json:"geo_ip_enabled" default:"true"`
	GeoIpProvider              string  `json:"geo_ip_provider" default:"ipinfo"`
	NezhaCompatEnabled         bool    `json:"nezha_compat_enabled" default:"false"`
	NezhaCompatListen          string  `json:"nezha_compat_listen" default:""`
	OAuthEnabled               bool    `json:"o_auth_enabled" default:"false"`
	OAuthProvider              string  `json:"o_auth_provider" default:"github"`
	DisablePasswordLogin       bool    `json:"disable_password_login" default:"false"`
	CloudflareTunnelToken      string  `json:"cloudflare_tunnel_token" default:""`
	CustomHead                 string  `json:"custom_head" default:""`
	CustomBody                 string  `json:"custom_body" default:""`
	NotificationEnabled        bool    `json:"notification_enabled" default:"true"`
	NotificationMethod         string  `json:"notification_method" default:"none"`
	NotificationTemplate       string  `json:"notification_template" default:"{{emoji}}{{emoji}}{{emoji}}\nEvent: {{event}}\nClients: {{client}}\nMessage: {{message}}\nTime: {{time}}"`
	ExpireNotificationEnabled  bool    `json:"expire_notification_enabled" default:"true"`
	ExpireNotificationLeadDays int     `json:"expire_notification_lead_days" default:"7"`
	LoginNotification          bool    `json:"login_notification" default:"true"`
	TrafficLimitPercentage     float64 `json:"traffic_limit_percentage" default:"80.00"`
	RecordEnabled              bool    `json:"record_enabled" default:"true"`
	RecordPreserveTime         int     `json:"record_preserve_time" default:"720"`
	PingRecordPreserveTime     int     `json:"ping_record_preserve_time" default:"24"`
	UpdatedAt                  time.Time
}

const (
	SitenameKey                   = "sitename"
	DescriptionKey                = "description"
	AllowCorsKey                  = "allow_cors"
	ThemeKey                      = "theme"
	PrivateSiteKey                = "private_site"
	ApiKeyKey                     = "api_key"
	AutoDiscoveryKeyKey           = "auto_discovery_key"
	ScriptDomainKey               = "script_domain"
	SendIpAddrToGuestKey          = "send_ip_addr_to_guest"
	EulaAcceptedKey               = "eula_accepted"
	BaseScriptsURLKey             = "base_scripts_url"
	GeoIpEnabledKey               = "geo_ip_enabled"
	GeoIpProviderKey              = "geo_ip_provider"
	NezhaCompatEnabledKey         = "nezha_compat_enabled"
	NezhaCompatListenKey          = "nezha_compat_listen"
	OAuthEnabledKey               = "o_auth_enabled"
	OAuthProviderKey              = "o_auth_provider"
	DisablePasswordLoginKey       = "disable_password_login"
	CloudflareTunnelTokenKey      = "cloudflare_tunnel_token"
	CustomHeadKey                 = "custom_head"
	CustomBodyKey                 = "custom_body"
	NotificationEnabledKey        = "notification_enabled"
	NotificationMethodKey         = "notification_method"
	NotificationTemplateKey       = "notification_template"
	ExpireNotificationEnabledKey  = "expire_notification_enabled"
	ExpireNotificationLeadDaysKey = "expire_notification_lead_days"
	LoginNotificationKey          = "login_notification"
	TrafficLimitPercentageKey     = "traffic_limit_percentage"
	RecordEnabledKey              = "record_enabled"
	RecordPreserveTimeKey         = "record_preserve_time"
	PingRecordPreserveTimeKey     = "ping_record_preserve_time"
	UpdatedAtKey                  = "updated_at"
)

func (Legacy) TableName() string {
	return "configs"
}
