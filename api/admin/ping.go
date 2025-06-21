package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/api"
	"github.com/komari-monitor/komari/database/tasks"
)

// POST body: clients []string, target, task_type string, interval int
func AddPingTask(c *gin.Context) {
	var req struct {
		Clients  []string `json:"clients" binding:"required"`
		Target   string   `json:"target" binding:"required"`
		TaskType string   `json:"type" binding:"required"`     // icmp, tcp, http
		Interval int      `json:"interval" binding:"required"` // 间隔时间，单位秒
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request data")
		return
	}

	if taskID, err := tasks.AddPingTask(req.Clients, req.Target, req.TaskType, req.Interval); err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
	} else {
		api.RespondSuccess(c, gin.H{"task_id": taskID})
	}
}

// POST body: id []uint
func DeletePingTask(c *gin.Context) {
	var req struct {
		ID []uint `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request data")
		return
	}

	if err := tasks.DeletePingTask(req.ID); err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
	} else {
		api.RespondSuccess(c, nil)
	}
}

// POST body: id []uint, updates map[string]interface{}
func EditPingTask(c *gin.Context) {
	var req struct {
		ID      []uint                 `json:"id" binding:"required"`
		Updates map[string]interface{} `json:"updates" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request data")
		return
	}

	if err := tasks.EditPingTask(req.ID, req.Updates); err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
	} else {
		tasks.DeletePingRecords(req.ID) // 重置记录
		api.RespondSuccess(c, nil)
	}
}

func GetAllPingTasks(c *gin.Context) {
	tasks, err := tasks.GetAllPingTasks()
	if err != nil {
		api.RespondError(c, http.StatusInternalServerError, err.Error())
		return
	}

	api.RespondSuccess(c, tasks)
}
