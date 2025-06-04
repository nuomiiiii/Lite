package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// StringArray represents a slice of strings stored as JSON in the database
type StringArray []string

func (sa *StringArray) Scan(value interface{}) error {
	bytes, ok := value.([]byte)
	if !ok {
		return fmt.Errorf("failed to scan StringArray: value is not []byte")
	}
	return json.Unmarshal(bytes, sa)
}

func (sa StringArray) Value() (driver.Value, error) {
	return json.Marshal(sa)
}

type Task struct {
	TaskId  string      `json:"task_id" gorm:"type:varchar(36);primaryKey;unique"`
	Clients StringArray `json:"clients" gorm:"type:longtext"`
	Command string      `json:"command" gorm:"type:text"`
}
type TaskResult struct {
	TaskId     string     `json:"task_id" gorm:"type:varchar(36)"`
	Client     string     `json:"client" gorm:"type:varchar(36)"`
	Result     string     `json:"result" gorm:"type:longtext"`
	ExitCode   *int       `json:"exit_code" gorm:"type:int"`
	FinishedAt *time.Time `json:"finished_at" gorm:"type:timestamp"`
	CreatedAt  time.Time  `json:"created_at" gorm:"type:timestamp"`
}
