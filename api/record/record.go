package record

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	records "github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/tasks"
)

func GetRecordsByUUID(c *gin.Context) {
	uuid := c.Query("uuid")
	hours := c.Query("hours")
	if uuid == "" {
		api.RespondError(c, 400, "UUID is required")
		return
	}
	if hours == "" {
		hours = "4"
	}

	hoursInt, err := strconv.Atoi(hours)
	if err != nil {
		api.RespondError(c, 400, "Invalid hours parameter")
		return
	}

	records, err := records.GetRecordsByClientAndTime(uuid, time.Now().Add(-time.Duration(hoursInt)*time.Hour), time.Now())
	if err != nil {
		api.RespondError(c, 500, "Failed to fetch records: "+err.Error())
		return
	}
	api.RespondSuccess(c, gin.H{
		"records": records,
		"count":   len(records),
	})
}

// GET query: uuid string, hours int
func GetPingRecords(c *gin.Context) {
	uuid := c.Query("uuid")
	hours := c.Query("hours")
	type RecordsResp struct {
		TaskId uint   `json:"task_id"`
		Time   string `json:"time"`
		Value  int    `json:"value"`
	}
	type Resp struct {
		Count   int           `json:"count"`
		Records []RecordsResp `json:"records"`
		Tasks   []gin.H       `json:"tasks,omitempty"` // 任务列表
	}

	if uuid == "" {
		api.RespondError(c, 400, "UUID is required")
		return
	}
	if hours == "" {
		hours = "4"
	}

	hoursInt, err := strconv.Atoi(hours)
	if err != nil {
		api.RespondError(c, 400, "Invalid hours parameter")
		return
	}

	records, err := tasks.GetPingRecordsByClientAndTime(uuid, time.Now().Add(-time.Duration(hoursInt)*time.Hour), time.Now())
	if err != nil {
		api.RespondError(c, 500, "Failed to fetch records: "+err.Error())
		return
	}

	response := &Resp{
		Count:   len(records),
		Records: []RecordsResp{},
	}
	for _, r := range records {
		response.Records = append(response.Records, RecordsResp{
			TaskId: r.TaskId,
			Time:   r.Time.ToTime().Format(time.RFC3339),
			Value:  r.Value,
		})
	}

	if len(records) > 0 {
		// 获取当前属于该 uuid 的 pingTasks
		pingTasks, err := tasks.GetAllPingTasks()
		if err != nil {
			api.RespondError(c, 500, "Failed to fetch ping tasks: "+err.Error())
			return
		}

		taskIdSet := make(map[uint]struct{})
		for _, r := range records {
			taskIdSet[r.TaskId] = struct{}{}
		}

		tasksList := make([]gin.H, 0, len(pingTasks))
		for _, t := range pingTasks {
			// 只保留分配给该 uuid 的任务
			found := false
			for _, client := range t.Clients {
				if client == uuid {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			// 只返回有记录的任务
			if _, ok := taskIdSet[t.Id]; ok {
				tasksList = append(tasksList, gin.H{
					"id":       t.Id,
					"name":     t.Name,
					"interval": t.Interval,
				})
			}
		}
		response.Tasks = tasksList
	}

	api.RespondSuccess(c, response)
}
