package tasks

import (
	"strings"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

// PublicReturnRoute is the visitor-facing snapshot for a node.
// It only carries current line names, assist-ping bindings, and suspected-block state.
type PublicReturnRoute struct {
	Client           string                  `json:"client"`
	Lines            []string                `json:"lines,omitempty"`
	Tasks            []PublicReturnRouteTask `json:"tasks,omitempty"`
	SuspectedBlocked bool                    `json:"suspected_blocked,omitempty"`
	HighConfidence   bool                    `json:"high_confidence,omitempty"`
}

// PublicReturnRouteTask maps a bound ping task to the current return-route line name.
type PublicReturnRouteTask struct {
	PingTaskID uint   `json:"ping_task_id,omitempty"`
	Line       string `json:"line,omitempty"`
}

type publicReturnRouteLineRow struct {
	Client      string
	CurrentLine string
	PingTaskID  *uint `gorm:"column:ping_task_id"`
	IPVersion   int   `gorm:"column:ip_version"`
}

type publicReturnRouteBlockRow struct {
	Client         string
	HighConfidence bool
}

func ListPublicReturnRoutes(includeHidden bool) ([]PublicReturnRoute, error) {
	return listPublicReturnRoutes(dbcore.GetDBInstance(), includeHidden)
}

func listPublicReturnRoutes(db *gorm.DB, includeHidden bool) ([]PublicReturnRoute, error) {
	out := []PublicReturnRoute{}
	if db == nil {
		return out, nil
	}

	hidden := map[string]struct{}{}
	if !includeHidden {
		var hiddenClients []models.Client
		if err := db.Model(&models.Client{}).Select("uuid").Where("hidden = ?", true).Find(&hiddenClients).Error; err != nil {
			return nil, err
		}
		for _, client := range hiddenClients {
			uuid := strings.TrimSpace(client.UUID)
			if uuid != "" {
				hidden[uuid] = struct{}{}
			}
		}
	}

	var lineRows []publicReturnRouteLineRow
	if err := db.Model(&models.ReturnRouteTask{}).
		Select("return_route_tasks.client AS client, return_route_statuses.current_line AS current_line, return_route_tasks.mainland_reachability_ping_task_id AS ping_task_id, return_route_tasks.ip_version AS ip_version").
		Joins("JOIN return_route_statuses ON return_route_statuses.task_id = return_route_tasks.id").
		Where("return_route_tasks.enabled = ?", true).
		Order("return_route_tasks.ip_version ASC, return_route_tasks.id ASC").
		Scan(&lineRows).Error; err != nil {
		return nil, err
	}

	var blockRows []publicReturnRouteBlockRow
	if err := db.Model(&models.ReturnRouteReachabilityStatus{}).
		Select("client, high_confidence").
		Where("display = ?", mainlandDisplaySuspectedBlocked).
		Find(&blockRows).Error; err != nil {
		return nil, err
	}

	index := map[string]int{}
	for _, row := range lineRows {
		client := strings.TrimSpace(row.Client)
		line := strings.TrimSpace(row.CurrentLine)
		if client == "" || line == "" {
			continue
		}
		if _, skip := hidden[client]; skip {
			continue
		}
		if pos, ok := index[client]; ok {
			out[pos].Lines = appendUniquePublicReturnLine(out[pos].Lines, line)
			out[pos].Tasks = appendPublicReturnTask(out[pos].Tasks, row.PingTaskID, line)
			continue
		}
		index[client] = len(out)
		out = append(out, PublicReturnRoute{
			Client: client,
			Lines:  []string{line},
			Tasks:  appendPublicReturnTask(nil, row.PingTaskID, line),
		})
	}

	for _, row := range blockRows {
		client := strings.TrimSpace(row.Client)
		if client == "" {
			continue
		}
		if _, skip := hidden[client]; skip {
			continue
		}
		pos, ok := index[client]
		if !ok {
			pos = len(out)
			index[client] = pos
			out = append(out, PublicReturnRoute{Client: client})
		}
		out[pos].SuspectedBlocked = true
		if row.HighConfidence {
			out[pos].HighConfidence = true
		}
	}

	return out, nil
}

func appendUniquePublicReturnLine(lines []string, line string) []string {
	for _, existing := range lines {
		if existing == line {
			return lines
		}
	}
	return append(lines, line)
}

func appendPublicReturnTask(tasks []PublicReturnRouteTask, pingID *uint, line string) []PublicReturnRouteTask {
	if pingID == nil || *pingID == 0 {
		return tasks
	}
	name := strings.TrimSpace(line)
	if name == "" {
		return tasks
	}
	id := *pingID
	for _, existing := range tasks {
		if existing.PingTaskID == id {
			return tasks
		}
	}
	return append(tasks, PublicReturnRouteTask{PingTaskID: id, Line: name})
}
