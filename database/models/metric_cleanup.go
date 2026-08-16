package models

import "time"

const (
	MetricCleanupEntity         = "entity"
	MetricCleanupPingTask       = "ping_task"
	MetricCleanupPingAssignment = "ping_assignment"
)

// MetricCleanupJob is the durable hand-off between the main database and the
// independent metric store. Business deletion and job creation commit in one
// main-database transaction; metric cleanup is idempotent and retried later.
type MetricCleanupJob struct {
	ID        uint64    `json:"id" gorm:"primaryKey;autoIncrement"`
	Kind      string    `json:"kind" gorm:"type:varchar(32);not null;uniqueIndex:idx_metric_cleanup_target"`
	EntityID  string    `json:"entity_id" gorm:"type:varchar(128);not null;default:'';uniqueIndex:idx_metric_cleanup_target"`
	TaskID    uint      `json:"task_id" gorm:"not null;default:0;uniqueIndex:idx_metric_cleanup_target"`
	Client    string    `json:"client" gorm:"type:varchar(128);not null;default:'';uniqueIndex:idx_metric_cleanup_target"`
	Attempts  int       `json:"attempts" gorm:"not null;default:0"`
	LastError string    `json:"last_error" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
