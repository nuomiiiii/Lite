package models

import "time"

// Notification 定义了通知相关的数据库模型
type OfflineNotification struct {
	Client     string `json:"client" gorm:"type:varchar(36);not null;index;unique;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:client;references:UUID"`
	ClientInfo Client `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID"`
	Enable     bool   `json:"enable" gorm:"type:boolean;default:false"`
	//Cooldown     int       `json:"cooldown" gorm:"type:int;not null;default:1800"`                // 冷却时间（秒），默认 30 分钟
	GracePeriod  int        `json:"grace_period" gorm:"type:int;not null;default:180"` // 宽限期（秒），默认 3 分钟
	LastNotified *time.Time `json:"last_notified"`                                     // 上次通知时间
}

// LoadNotification 定义了基于资源占用达标时间比的负载通知规则
type LoadNotification struct {
	Id           uint        `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Name         string      `json:"name" gorm:"type:varchar(255)"`
	Clients      StringArray `json:"clients" gorm:"type:longtext"`
	Metric       string      `json:"metric" gorm:"type:varchar(50);not null;default:'cpu'"`     // 监控指标，如 cpu, ram, load
	Threshold    float32     `json:"threshold" gorm:"type:decimal(5,2);not null;default:80.00"` // 阈值百分比
	Ratio        float32     `json:"ratio" gorm:"type:decimal(5,2);not null;default:0.80"`      // 达标时间比
	Interval     int         `json:"interval" gorm:"type:int;not null;default:15"`              // 监测间隔（分钟）
	LastNotified *time.Time  `json:"last_notified"`                                             // 上次通知时间
}

// TrafficReportNotification 定义了流量定时报告的数据库模型
type TrafficReportNotification struct {
	Client         string `json:"client" gorm:"type:varchar(36);not null;index;unique;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:client;references:UUID"`
	ClientInfo     Client `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID"`
	Enable         bool   `json:"enable" gorm:"type:boolean;default:false"`
	Daily          bool   `json:"daily" gorm:"type:boolean;default:false"`           // 日报
	Weekly         bool   `json:"weekly" gorm:"type:boolean;default:false"`          // 周报
	Monthly        bool   `json:"monthly" gorm:"type:boolean;default:false"`         // 月报
	IncludeTraffic bool   `json:"include_traffic" gorm:"type:boolean;default:true"`  // 上行/下行流量
	IncludeBilling bool   `json:"include_billing" gorm:"type:boolean;default:false"` // 按服务器计费规则计算的流量
}

// TrafficDailyLedger stores exact report traffic for one Beijing calendar day.
// The daily ledger is intentionally separate from the general metric store so
// weekly and monthly reports do not require long retention for four metrics.
type TrafficDailyLedger struct {
	Client     string    `json:"client" gorm:"type:varchar(36);primaryKey;not null"`
	ClientInfo Client    `json:"-" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Day        string    `json:"day" gorm:"type:varchar(10);primaryKey;not null"`
	UpBytes    int64     `json:"up_bytes" gorm:"type:bigint;not null;default:0"`
	DownBytes  int64     `json:"down_bytes" gorm:"type:bigint;not null;default:0"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TrafficCalibrationAdjustment stores one auditable traffic correction
// allocated to a Beijing calendar day. Raw Agent metrics remain unchanged.
type TrafficCalibrationAdjustment struct {
	ID            uint64    `json:"id" gorm:"primaryKey;autoIncrement"`
	CalibrationID string    `json:"calibration_id" gorm:"type:varchar(32);not null;index;uniqueIndex:idx_traffic_calibration_day"`
	Client        string    `json:"client" gorm:"type:varchar(36);not null;index;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:Client;references:UUID"`
	ClientInfo    Client    `json:"-" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Cycle         string    `json:"cycle" gorm:"type:varchar(10);not null;index"`
	Day           string    `json:"day" gorm:"type:varchar(10);not null;index;uniqueIndex:idx_traffic_calibration_day"`
	UpDelta       int64     `json:"up_delta" gorm:"type:bigint;not null;default:0"`
	DownDelta     int64     `json:"down_delta" gorm:"type:bigint;not null;default:0"`
	TargetUp      int64     `json:"target_up" gorm:"type:bigint;not null;default:0"`
	TargetDown    int64     `json:"target_down" gorm:"type:bigint;not null;default:0"`
	Operator      string    `json:"operator,omitempty" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt     time.Time `json:"created_at" gorm:"index"`
}

// PingLossNotification defines packet-loss alerts for one client and ping task.
type PingLossNotification struct {
	Id              uint       `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Client          string     `json:"client" gorm:"type:varchar(36);not null;uniqueIndex:idx_ping_loss_notification_target"`
	ClientInfo      Client     `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	TaskId          uint       `json:"task_id" gorm:"not null;uniqueIndex:idx_ping_loss_notification_target"`
	Task            PingTask   `json:"task,omitempty" gorm:"foreignKey:TaskId;references:Id;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Enable          bool       `json:"enable" gorm:"type:boolean;default:false"`
	WindowSeconds   int        `json:"window_seconds" gorm:"type:int;not null;default:60"`
	LossThreshold   float64    `json:"loss_threshold" gorm:"type:decimal(5,2);not null;default:5.00"`
	MinimumSamples  int        `json:"minimum_samples" gorm:"type:int;not null;default:1"`
	CooldownSeconds int        `json:"cooldown_seconds" gorm:"type:int;not null;default:300"`
	LastNotified    *time.Time `json:"last_notified"`
	AlertActive     bool       `json:"alert_active" gorm:"type:boolean;not null;default:false"`
}
