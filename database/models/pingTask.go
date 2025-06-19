package models

import "time"

type PingRecord struct {
	Client string    `json:"client" gorm:"type:varchar(36);not null;index;unique;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:client;references:UUID"`
	Type   string    `json:"type" gorm:"type:varchar(12);not null;default:'ping'"` // ping tcping http
	Time   time.Time `json:"time" gorm:"index;not null;unique"`
	Target string    `json:"target" gorm:"type:varchar(255);not null"`
	Value  int       `json:"value" gorm:"type:int;not null"` // Ping 值，单位毫秒
}

type PingTask struct {
	ID       uint        `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Clients  StringArray `json:"clients" gorm:"type:longtext"`
	Type     string      `json:"type" gorm:"type:varchar(12);not null;default:'ping'"` // ping tcping http
	Target   string      `json:"target" gorm:"type:varchar(255);not null"`
	Interval int         `json:"interval" gorm:"type:int;not null;default:60"` // 间隔时间
}
