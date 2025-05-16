package api

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/common"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/utils"
)

var (
	Records map[string][]common.Report = make(map[string][]common.Report)
)

type TerminalSession struct {
	UUID    string
	Browser *websocket.Conn
	Agent   *websocket.Conn
}

var TerminalSessionsMutex = &sync.Mutex{}
var TerminalSessions = make(map[string]*TerminalSession)

func SaveClientReportToDB() error {
	lastMinute := time.Now().Add(-time.Minute * 1).Unix()
	record := []models.Record{}
	// 遍历所有的客户端记录
	for uuid, reports := range Records {
		// 删除一分钟前的记录
		filtered := reports[:0]
		for _, r := range reports {
			if r.UpdatedAt.Unix() >= lastMinute {
				filtered = append(filtered, r)
			}
		}
		Records[uuid] = filtered
		r := utils.AverageReport(uuid, time.Now(), filtered)

		record = append(record, r)

		db := dbcore.GetDBInstance()
		err := db.Model(&models.Record{}).Where("client = ?", uuid).Create(record).Error
		if err != nil {
			return err
		}
	}

	return nil
}
