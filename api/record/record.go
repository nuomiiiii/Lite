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

// GET query: uuid string OR task_id int, hours int
func GetPingRecords(c *gin.Context) {
	uuid := c.Query("uuid")
	taskIdStr := c.Query("task_id")

	// 必须提供 uuid 或 task_id 其中之一
	if uuid == "" && taskIdStr == "" {
		api.RespondError(c, 400, "UUID or task_id is required")
		return
	}

	// 登录状态检查
	isLogin := false
	session, _ := c.Cookie("session_token")
	_, err := accounts.GetUserBySession(session)
	if err == nil {
		isLogin = true
	}

	// 仅在未登录时需要 Hidden 信息做过滤（仅当按 uuid 查询时）
	if uuid != "" && !isLogin {
		var hiddenClients []models.Client
		db := dbcore.GetDBInstance()
		_ = db.Select("uuid").Where("hidden = ?", true).Find(&hiddenClients).Error
		hiddenMap := map[string]bool{}
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
		TaskId uint   `json:"task_id,omitempty"` // 按 task_id 查询时不返回
		Time   string `json:"time"`
		Value  int    `json:"value"`
		Client string `json:"client,omitempty"` // 当按 task_id 查询时返回
	}
	type ClientBasicInfo struct {
		Client string  `json:"client"`
		Loss   float64 `json:"loss"`
		Min    int     `json:"min"`  // 最小延迟（毫秒）
		Max    int     `json:"max"`  // 最大延迟（毫秒）
	}
	type Resp struct {
		Count      int               `json:"count"`
		BasicInfo  []ClientBasicInfo `json:"basic_info,omitempty"`  // 各客户端基础信息（按 task_id 查询时返回）
		Records    []RecordsResp     `json:"records"`
		Tasks      []gin.H           `json:"tasks,omitempty"`       // 任务列表（按 uuid 查询时返回）
	}

	if hours == "" {
		hours = "4"
	}

	hoursInt, err := strconv.Atoi(hours)
	if err != nil {
		api.RespondError(c, 400, "Invalid hours parameter")
		return
	}

	var records []models.PingRecord
	startTime := time.Now().Add(-time.Duration(hoursInt) * time.Hour)
	endTime := time.Now()

	// 根据查询类型获取记录
	if taskIdStr != "" {
		// 按 task_id 查询
		taskId, err := strconv.Atoi(taskIdStr)
		if err != nil {
			api.RespondError(c, 400, "Invalid task_id parameter")
			return
		}
		records, err = tasks.GetPingRecordsByTaskAndTime(uint(taskId), startTime, endTime)
		if err != nil {
			api.RespondError(c, 500, "Failed to fetch records: "+err.Error())
			return
		}
	} else {
		// 按 uuid 查询（原有逻辑）
		records, err = tasks.GetPingRecordsByClientAndTime(uuid, startTime, endTime)
		if err != nil {
			api.RespondError(c, 500, "Failed to fetch records: "+err.Error())
			return
		}
	}

	response := &Resp{
		Count:   len(records),
		Records: []RecordsResp{},
	}
	
	// 用于统计每个客户端的信息（按 task_id 查询时使用）
	clientStats := make(map[string]struct {
		total int
		loss  int
		min   int
		max   int
	})
	
	for _, r := range records {
		rec := RecordsResp{
			Time:  r.Time.ToTime().Format(time.RFC3339),
			Value: r.Value,
		}
		
		// 如果是按 task_id 查询，返回 client 信息，不返回 task_id
		if taskIdStr != "" {
			rec.Client = r.Client
			// 统计每个客户端的信息
			stats := clientStats[r.Client]
			stats.total++
			
			if r.Value < 0 {
				// ping 失败
				stats.loss++
			} else {
				// ping 成功，更新 min/max
				if stats.min == 0 || r.Value < stats.min {
					stats.min = r.Value
				}
				if r.Value > stats.max {
					stats.max = r.Value
				}
			}
			clientStats[r.Client] = stats
		} else {
			// 按 uuid 查询时返回 task_id
			rec.TaskId = r.TaskId
		}
		
		response.Records = append(response.Records, rec)
	}
	
	// 如果是按 task_id 查询，计算每个客户端的统计信息
	if taskIdStr != "" && len(clientStats) > 0 {
		response.BasicInfo = make([]ClientBasicInfo, 0, len(clientStats))
		for client, stats := range clientStats {
			loss := float64(0)
			if stats.total > 0 {
				loss = float64(stats.loss) / float64(stats.total) * 100
			}
			response.BasicInfo = append(response.BasicInfo, ClientBasicInfo{
				Client: client,
				Loss:   loss,
				Min:    stats.min,
				Max:    stats.max,
			})
		}
	}

	// 只有按 uuid 查询时才返回 tasks 列表
	if uuid != "" && len(records) >= 0 {
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
