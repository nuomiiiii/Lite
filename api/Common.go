package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/common"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
)

var (
	Records = utils.NewSafeMap[string, []common.Report]()
)

type TerminalSession struct {
	UUID     string
	UserUUID string
	Browser  *websocket.Conn
	Agent    *websocket.Conn
}

var TerminalSessionsMutex = &sync.Mutex{}
var TerminalSessions = make(map[string]*TerminalSession)

func SaveClientReportToDB() error {
	lastMinute := time.Now().Add(-time.Minute * 1).Unix()
	record := []models.Record{}
	var err error

	// 遍历所有的客户端记录
	Records.Range(func(uuid string, reports []common.Report) bool {
		// 删除一分钟前的记录
		filtered := reports[:0]
		for _, r := range reports {
			if r.UpdatedAt.Unix() >= lastMinute {
				filtered = append(filtered, r)
			}
		}
		Records.Store(uuid, filtered)

		if uuid == "" {
			return true
		}

		r := utils.AverageReport(uuid, time.Now(), filtered)
		record = append(record, r)

		db := dbcore.GetDBInstance()
		err = db.Model(&models.Record{}).Create(record).Error
		if err != nil {
			return false
		}
		return true
	})

	return err
}

type Response struct {
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Respond sends a standardized JSON response.
func Respond(c *gin.Context, httpStatus int, status string, message string, data interface{}) {
	c.JSON(httpStatus, Response{Status: status, Message: message, Data: data})
}

// RespondSuccess sends a success response with data.
func RespondSuccess(c *gin.Context, data interface{}) {
	Respond(c, http.StatusOK, "success", "", data)
}

// RespondSuccessMessage sends a success response with message and data.
func RespondSuccessMessage(c *gin.Context, message string, data interface{}) {
	Respond(c, http.StatusOK, "success", message, data)
}

// RespondError sends an error response with message.
func RespondError(c *gin.Context, httpStatus int, message string) {
	Respond(c, httpStatus, "error", message, nil)
}
func GetVersion(c *gin.Context) {
	RespondSuccess(c, gin.H{
		"version": utils.CurrentVersion,
		"hash":    utils.VersionHash,
	})
}
