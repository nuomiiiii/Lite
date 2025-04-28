package common

import "time"

type Message struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	Sender    string `json:"sender"`
	Timestamp int64  `json:"timestamp"`
}

type ClientConfig struct {
	ClientUUID  string    `json:"client_uuid" gorm:"type:uuid;primaryKey;foreignKey:ClientUUID;references:UUID;constraint:OnDelete:CASCADE"`
	CPU         bool      `json:"cpu" gorm:"default:true"`
	GPU         bool      `json:"gpu" gorm:"default:true"`
	RAM         bool      `json:"ram" gorm:"default:true"`
	SWAP        bool      `json:"swap" gorm:"default:true"`
	LOAD        bool      `json:"load" gorm:"default:true"`
	UPTIME      bool      `json:"uptime" gorm:"default:true"`
	TEMP        bool      `json:"temp" gorm:"default:true"`
	OS          bool      `json:"os" gorm:"default:true"`
	DISK        bool      `json:"disk" gorm:"default:true"`
	NET         bool      `json:"net" gorm:"default:true"`
	PROCESS     bool      `json:"process" gorm:"default:true"`
	Connections bool      `json:"connections" gorm:"default:true"`
	Interval    int       `json:"interval" gorm:"default:3;check:Interval >= 1"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ClientInfo stores static client information
type ClientInfo struct {
	ClientUUID string `json:"uuid,omitempty" gorm:"type:uuid;primaryKey;foreignKey:ClientUUID;references:UUID;constraint:OnDelete:CASCADE"`
	ClientName string `json:"name" gorm:"type:varchar(100);not null"`
	CPUNAME    string `json:"cpu_name" gorm:"type:varchar(100)"`
	CPUARCH    string `json:"arch" gorm:"type:varchar(50)"`
	CPUCORES   int    `json:"cpu_cores" gorm:"type:int"`
	OS         string `json:"os" gorm:"type:varchar(100)"`
	GPUNAME    string `json:"gpu_name" gorm:"type:varchar(100)"`
	IPv4       string `json:"ipv4,omitempty" gorm:"type:varchar(100)"`
	IPv6       string `json:"ipv6,omitempty" gorm:"type:varchar(100)"`
	Country    string `json:"country" gorm:"type:varchar(100)"`
	Remark     string `json:"remark,omitempty" gorm:"type:longtext"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type IPAddress struct {
	Ipv4 string `json:"ipv4"`
	Ipv6 string `json:"ipv6"`
}
type Report struct {
	UUID        string            `json:"uuid,omitempty"`
	CPU         CPUReport         `json:"cpu"`
	Ram         RamReport         `json:"ram"`
	Swap        RamReport         `json:"swap"`
	Load        LoadReport        `json:"load"`
	Disk        DiskReport        `json:"disk"`
	Network     NetworkReport     `json:"network"`
	Connections ConnectionsReport `json:"connections"`
	Uptime      int64             `json:"uptime"`
	Process     int               `json:"process"`
	Message     string            `json:"message"`
	Method      string            `json:"method"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type CPUReport struct {
	Name  string  `json:"name"`
	Cores int     `json:"cores"`
	Arch  string  `json:"arch"`
	Usage float64 `json:"usage"`
}

type GPUReport struct {
	Name  string  `json:"name"`
	Usage float64 `json:"usage"`
}

type RamReport struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

type LoadReport struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

type DiskReport struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

type NetworkReport struct {
	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	TotalUp   int64 `json:"totalUp"`
	TotalDown int64 `json:"totalDown"`
}

type ConnectionsReport struct {
	TCP int `json:"tcp"`
	UDP int `json:"udp"`
}
