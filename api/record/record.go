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

	response := gin.H{
		"records": records,
		"count":   len(records),
	}

	if len(records) > 0 {
		pingTasks, err := tasks.GetAllPingTasks()
		if err != nil {
			api.RespondError(c, 500, "Failed to fetch ping tasks: "+err.Error())
			return
		}
		tasksList := make([]gin.H, 0, len(pingTasks))
		for _, t := range pingTasks {
			tasksList = append(tasksList, gin.H{
				"id":   t.Id,
				"name": t.Name,
			})
		}
		response["tasks"] = tasksList
	}

	api.RespondSuccess(c, response)
}
