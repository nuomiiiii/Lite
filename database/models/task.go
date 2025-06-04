package models

type Task struct {
	TaskId string `json:"task_id" gorm:"type:varchar(36),primaryKey,uniqueIndex:idx_tasks_task_id"`
	// Clients is a JSON array of client UUIDs
	Clients string `json:"clients" gorm:"type:longtext"`
	Command string `json:"command" gorm:"type:text"`
}
type TaskResult struct {
	TaskId     string `json:"task_id" gorm:"type:varchar(36)"`
	Client     string `json:"client" gorm:"type:varchar(36)"`
	Result     string `json:"result" gorm:"type:longtext"`
	ExitCode   *int   `json:"exit_code" gorm:"type:int,"`
	FinishedAt string `json:"finished_at" gorm:"type:timestamp"`
	CreatedAt  string `json:"created_at" gorm:"type:timestamp"`
}
