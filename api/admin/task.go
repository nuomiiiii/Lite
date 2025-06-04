package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/tasks"
)

func GetTasks(c *gin.Context) {
	dbTasks, err := tasks.GetAllTasks()
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve tasks: " + err.Error()})
		return
	}
	var responseTasks []gin.H
	for _, t := range dbTasks {
		results, err := tasks.GetTaskResultsByTaskId(t.TaskId)
		if err != nil {
			c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve task results: " + err.Error()})
			return
		}

		var filteredResults []gin.H
		for _, r := range results {
			filteredResults = append(filteredResults, gin.H{
				"client":      r.Client,
				"result":      r.Result,
				"exit_code":   r.ExitCode,
				"finished_at": r.FinishedAt,
				"created_at":  r.CreatedAt,
			})
		}

		responseTasks = append(responseTasks, gin.H{
			"task_id": t.TaskId,
			"clients": t.Clients,
			"command": t.Command,
			"results": filteredResults,
		})
	}
	c.JSON(200, gin.H{"status": "success", "tasks": responseTasks})
}

func GetTaskById(c *gin.Context) {
	taskId := c.Param("task_id")
	if taskId == "" {
		c.JSON(400, gin.H{"status": "error", "message": "Task ID is required"})
		return
	}
	task, err := tasks.GetTaskByTaskId(taskId)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve task: " + err.Error()})
		return
	}
	if task == nil {
		c.JSON(404, gin.H{"status": "error", "message": "Task not found"})
		return
	}
	results, err := tasks.GetTaskResultsByTaskId(taskId)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve task results: " + err.Error()})
		return
	}
	var filteredResults []gin.H
	for _, r := range results {
		filteredResults = append(filteredResults, gin.H{
			"client":      r.Client,
			"result":      r.Result,
			"exit_code":   r.ExitCode,
			"finished_at": r.FinishedAt,
			"created_at":  r.CreatedAt,
		})
	}
	c.JSON(200, gin.H{"status": "success", "task": gin.H{
		"task_id": task.TaskId,
		"clients": task.Clients,
		"command": task.Command,
		"results": filteredResults,
	}})
}

func GetTasksByClientId(c *gin.Context) {
	clientId := c.Param("uuid")
	if clientId == "" {
		c.JSON(400, gin.H{"status": "error", "message": "Client ID is required"})
		return
	}
	tasks, err := tasks.GetTasksByClientId(clientId)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve tasks: " + err.Error()})
		return
	}
	if len(tasks) == 0 {
		c.JSON(404, gin.H{"status": "error", "message": "No tasks found for this client"})
		return
	}
	c.JSON(200, gin.H{"status": "success", "tasks": tasks})
}

func GetSpecificTaskResult(c *gin.Context) {
	taskId := c.Param("task_id")
	clientId := c.Param("uuid")
	if taskId == "" || clientId == "" {
		c.JSON(400, gin.H{"status": "error", "message": "Task ID and Client ID are required"})
		return
	}
	result, err := tasks.GetSpecificTaskResult(taskId, clientId)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve task result: " + err.Error()})
		return
	}
	if result == nil {
		c.JSON(404, gin.H{"status": "error", "message": "No result found for this task and client"})
		return
	}
	c.JSON(200, gin.H{"status": "success", "result": result})
}

// Param: task_id
func GetTaskResultsByTaskId(c *gin.Context) {
	taskId := c.Param("task_id")
	if taskId == "" {
		c.JSON(400, gin.H{"status": "error", "message": "Task ID is required"})
		return
	}
	results, err := tasks.GetTaskResultsByTaskId(taskId)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve task results: " + err.Error()})
		return
	}
	if len(results) == 0 {
		c.JSON(404, gin.H{"status": "error", "message": "No results found for this task"})
		return
	}
	c.JSON(200, gin.H{"status": "success", "results": results})
}

func GetAllTaskResultByUUID(c *gin.Context) {
	clientId := c.Param("uuid")
	if clientId == "" {
		c.JSON(400, gin.H{"status": "error", "message": "Client ID is required"})
		return
	}
	results, err := tasks.GetAllTasksResultByUUID(clientId)
	if err != nil {
		c.JSON(500, gin.H{"status": "error", "message": "Failed to retrieve tasks: " + err.Error()})
		return
	}
	if len(results) == 0 {
		c.JSON(404, gin.H{"status": "error", "message": "No tasks found for this client"})
		return
	}
	c.JSON(200, gin.H{"status": "success", "tasks": results})
}
