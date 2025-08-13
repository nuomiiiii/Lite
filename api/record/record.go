package record

import (
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	records "github.com/komari-monitor/komari/database/records"
	"github.com/komari-monitor/komari/database/tasks"
)

func GetRecordsByUUID(c *gin.Context) {
	uuid := c.Query("uuid")

	// 登录状态检查
	isLogin := false
	session, _ := c.Cookie("session_token")
	_, err := accounts.GetUserBySession(session)
	if err == nil {
		isLogin = true
	}

	// 仅在未登录时需要 Hidden 信息做过滤
	hiddenMap := map[string]bool{}
	if !isLogin {
		var hiddenClients []models.Client
		db := dbcore.GetDBInstance()
		_ = db.Select("uuid").Where("hidden = ?", true).Find(&hiddenClients).Error
		for _, cli := range hiddenClients {
			hiddenMap[cli.UUID] = true
		}

		if hiddenMap[uuid] {
			api.RespondError(c, 400, "UUID is required") //防止未登录用户获取隐藏客户端数据
			return
		}
	}

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

	if uuid == "" {
		api.RespondError(c, 400, "UUID is required")
		return
	}

	// 登录状态检查
	isLogin := false
	session, _ := c.Cookie("session_token")
	_, err := accounts.GetUserBySession(session)
	if err == nil {
		isLogin = true
	}

	// 仅在未登录时需要 Hidden 信息做过滤
	hiddenMap := map[string]bool{}
	if !isLogin {
		var hiddenClients []models.Client
		db := dbcore.GetDBInstance()
		_ = db.Select("uuid").Where("hidden = ?", true).Find(&hiddenClients).Error
		for _, cli := range hiddenClients {
			hiddenMap[cli.UUID] = true
		}

		if hiddenMap[uuid] {
			api.RespondError(c, 400, "UUID is required") //防止未登录用户获取隐藏客户端数据
			return
		}
	}

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

	if len(records) >= 0 {
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
			found := slices.Contains(t.Clients, uuid)
			if !found {
				continue
			}

			// 计算该任务的丢包率
			totalCount := 0
			lossCount := 0
			for _, r := range records {
				if r.TaskId == t.Id {
					totalCount++
					if r.Value < 0 {
						lossCount++
					}
				}
			}

			var lossRate float64 = 0
			if totalCount > 0 {
				lossRate = float64(lossCount) / float64(totalCount) * 100
			}

			tasksList = append(tasksList, gin.H{
				"id":       t.Id,
				"name":     t.Name,
				"interval": t.Interval,
				"loss":     lossRate,
			})
		}
		response.Tasks = tasksList
	}

	api.RespondSuccess(c, response)
}
