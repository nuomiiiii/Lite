package models

type PingRecord struct {
	Client string  `json:"client" gorm:"type:varchar(36);not null;index;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:client;references:UUID"`
	TaskId uint    `json:"task_id" gorm:"not null;index;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:TaskId;references:Id"`
	Time   UTCTime `json:"time" gorm:"index;not null"`
	Value  int     `json:"value" gorm:"type:int;not null"` // Ping 值，单位毫秒
}

type PingTask struct {
	Id       uint        `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Name     string      `json:"name" gorm:"type:varchar(255);not null;index"`
	Clients  StringArray `json:"clients" gorm:"type:longtext"`
	Type     string      `json:"type" gorm:"type:varchar(12);not null;default:'icmp'"` // icmp tcp http
	Target   string      `json:"target" gorm:"type:varchar(255);not null"`
	Interval int         `json:"interval" gorm:"type:int;not null;default:60"` // 间隔时间
}
