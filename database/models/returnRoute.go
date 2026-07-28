package models

import "time"

// ReturnRouteTask defines one carrier route check executed by a single agent.
type ReturnRouteTask struct {
	Id              uint      `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Name            string    `json:"name" gorm:"type:varchar(255);not null"`
	Client          string    `json:"client" gorm:"type:varchar(36);not null;index"`
	ClientInfo      Client    `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Carrier         string    `json:"carrier" gorm:"type:varchar(16);not null"`
	Region          string    `json:"region" gorm:"type:varchar(64);not null"`
	Target          string    `json:"target" gorm:"type:varchar(255);not null"`
	IPVersion       int       `json:"ip_version" gorm:"type:int;not null;default:4"`
	ExpectedLine    string    `json:"expected_line" gorm:"type:varchar(32);not null"`
	Protocol        string    `json:"protocol" gorm:"type:varchar(12);not null;default:'icmp'"`
	Interval        int       `json:"interval" gorm:"type:int;not null;default:180"`
	SwitchConfirm   int       `json:"switch_confirm" gorm:"type:int;not null;default:2"`
	RecoveryConfirm int       `json:"recovery_confirm" gorm:"type:int;not null;default:3"`
	Cooldown        int       `json:"cooldown" gorm:"type:int;not null;default:1800"`
	Notify          bool      `json:"notify" gorm:"type:boolean;not null;default:true"`
	NotifyRecovery  bool      `json:"notify_recovery" gorm:"type:boolean;not null;default:true"`
	Enabled         bool      `json:"enabled" gorm:"type:boolean;not null;default:true"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ReturnRouteStatus stores only the latest observation and state-machine data.
type ReturnRouteStatus struct {
	TaskId         uint            `json:"task_id" gorm:"primaryKey"`
	Task           ReturnRouteTask `json:"-" gorm:"foreignKey:TaskId;references:Id;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	CurrentLine    string          `json:"current_line" gorm:"type:varchar(32)"`
	State          string          `json:"state" gorm:"type:varchar(16);not null;default:'pending'"`
	Confidence     float64         `json:"confidence" gorm:"type:decimal(5,4);not null;default:0"`
	ASNPath        StringArray     `json:"asn_path" gorm:"type:longtext"`
	RoutePath      StringArray     `json:"route_path" gorm:"type:longtext"`
	CandidateLine  string          `json:"candidate_line" gorm:"type:varchar(32)"`
	CandidateCount int             `json:"candidate_count" gorm:"type:int;not null;default:0"`
	LastError      string          `json:"last_error" gorm:"type:varchar(255)"`
	LastCheckedAt  *time.Time      `json:"last_checked_at"`
	LastChangedAt  *time.Time      `json:"last_changed_at"`
	LastNotifiedAt *time.Time      `json:"last_notified_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// ReturnRouteEvent is written only for confirmed switches and recoveries.
type ReturnRouteEvent struct {
	Id           uint            `json:"id" gorm:"primaryKey;autoIncrement"`
	TaskId       uint            `json:"task_id" gorm:"not null;index"`
	Task         ReturnRouteTask `json:"-" gorm:"foreignKey:TaskId;references:Id;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Client       string          `json:"client" gorm:"type:varchar(36);not null;index"`
	TaskName     string          `json:"task_name" gorm:"type:varchar(255)"`
	Carrier      string          `json:"carrier" gorm:"type:varchar(16);index"`
	Region       string          `json:"region" gorm:"type:varchar(64);index"`
	Target       string          `json:"target" gorm:"type:varchar(255)"`
	IPVersion    int             `json:"ip_version" gorm:"type:int"`
	ExpectedLine string          `json:"expected_line" gorm:"type:varchar(32);index"`
	Kind         string          `json:"kind" gorm:"type:varchar(16);not null;index"`
	FromLine     string          `json:"from_line" gorm:"type:varchar(32)"`
	ToLine       string          `json:"to_line" gorm:"type:varchar(32);not null;index"`
	Confidence   float64         `json:"confidence" gorm:"type:decimal(5,4);not null"`
	ASNPath      StringArray     `json:"asn_path" gorm:"type:longtext"`
	RoutePath    StringArray     `json:"route_path" gorm:"type:longtext"`
	OccurredAt   time.Time       `json:"occurred_at" gorm:"not null;index"`
}
