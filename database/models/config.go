package models

type Config struct {
	ID          uint   `json:"id,omitempty" gorm:"primaryKey;autoIncrement"` // 1
	Sitename    string `json:"sitename" gorm:"type:varchar(100);not null"`
	Description string `json:"description" gorm:"type:text"`
	AllowCors   bool   `json:"allow_cors" gorm:"column:allow_cors;default:false"`
	Theme       string `json:"theme" gorm:"type:varchar(100);default:'default'"` // 主题名称，默认 'default'
	PrivateSite bool   `json:"private_site" gorm:"default:false"`                // 是否为私有站点，默认 false
	// GeoIP 配置
	GeoIpEnabled  bool   `json:"geo_ip_enabled" gorm:"default:true"`
	GeoIpProvider string `json:"geo_ip_provider" gorm:"type:varchar(20);default:'ip-api'"` // empty, mmdb, ip-api, geojs
	// OAuth 配置
	OAuthEnabled         bool   `json:"o_auth_enabled" gorm:"default:false"`
	OAuthProvider        string `json:"o_auth_provider" gorm:"type:varchar(50);default:'github'"`
	DisablePasswordLogin bool   `json:"disable_password_login" gorm:"default:false"`
	// 自定义美化
	CustomHead string `json:"custom_head" gorm:"type:longtext"`
	CustomBody string `json:"custom_body" gorm:"type:longtext"`
	// 通知
	NotificationEnabled        bool   `json:"notification_enabled" gorm:"default:false"` // 通知总开关
	NotificationMethod         string `json:"notification_method" gorm:"type:varchar(64);default:'none'"`
	ExpireNotificationEnabled  bool   `json:"expire_notification_enabled" gorm:"default:false"` // 是否启用过期通知
	ExpireNotificationLeadDays int    `json:"expire_notification_lead_days" gorm:"default:7"`   // 过期前多少天通知，默认7天
	// Record
	RecordEnabled          bool `json:"record_enabled" gorm:"default:true"`          // 是否启用记录功能
	RecordPreserveTime     int  `json:"record_preserve_time" gorm:"default:720"`     // 记录保留时间，单位小时，默认30天
	PingRecordPreserveTime int  `json:"ping_record_preserve_time" gorm:"default:24"` // Ping 记录保留时间，单位小时，默认1天
	CreatedAt              LocalTime
	UpdatedAt              LocalTime
}
