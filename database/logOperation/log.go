package logOperation

import (
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
)

func Log(ip, uuid, message, msgType string) {
	now := time.Now()
	db := dbcore.GetDBInstance()
	logEntry := &models.Log{
		IP:      ip,
		UUID:    uuid,
		Message: message,
		MsgType: msgType,
		Time:    now,
	}
	db.Create(logEntry)
}
