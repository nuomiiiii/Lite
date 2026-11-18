package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/trafficledger"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

const wsClientsSnapshotTTL = 500 * time.Millisecond

type wsClientsSnapshotKey struct {
	db       *gorm.DB
	cacheGen uint64
	isLogin  bool
	group    string
}

type wsClientsSnapshotEntry struct {
	payload []byte
	expires time.Time
}

var (
	wsSnapshotMu     sync.RWMutex
	wsSnapshotCache  = make(map[wsClientsSnapshotKey]wsClientsSnapshotEntry)
	wsSnapshotFlight singleflight.Group
)

type wsClientsPayload struct {
	Online []string             `json:"online"`
	Data   map[string]v2.Report `json:"data"`
}

func loadWSHiddenAndGroupMaps(db *gorm.DB, needHidden, needGroup bool, group, targetUUID string) (map[string]bool, map[string]bool) {
	if (!needHidden && !needGroup) || db == nil {
		return nil, nil
	}
	var hiddenMap map[string]bool
	if needHidden {
		hiddenMap = make(map[string]bool)
	}
	var groupMap map[string]bool
	if needGroup {
		groupMap = make(map[string]bool)
	}
	q := db.Model(&models.Client{})
	if targetUUID != "" {
		q = q.Where("uuid = ?", targetUUID)
	}
	switch {
	case needHidden && !needGroup:
		var uuids []string
		_ = q.Where("hidden = ?", true).Pluck("uuid", &uuids).Error
		for _, id := range uuids {
			hiddenMap[id] = true
		}
	case !needHidden && needGroup:
		var uuids []string
		_ = q.Where("\"group\" = ?", group).Pluck("uuid", &uuids).Error
		for _, id := range uuids {
			groupMap[id] = true
		}
	default:
		var rows []struct {
			UUID   string
			Hidden bool
			Group  string
		}
		_ = q.Select("uuid", "hidden", "group").Where("hidden = ? OR \"group\" = ?", true, group).Scan(&rows).Error
		for _, cli := range rows {
			if cli.Hidden {
				hiddenMap[cli.UUID] = true
			}
			if cli.Group == group {
				groupMap[cli.UUID] = true
			}
		}
	}
	return hiddenMap, groupMap
}

func buildWSClientsPayload(ctx context.Context, db *gorm.DB, isLogin bool, group, targetUUID string) wsClientsPayload {
	resp := wsClientsPayload{
		Online: []string{},
		Data:   map[string]v2.Report{},
	}
	needHidden := !isLogin
	needGroup := group != ""
	hiddenMap, groupMap := loadWSHiddenAndGroupMaps(db, needHidden, needGroup, group, targetUUID)

	for _, key := range agent_runtime.GetAllOnlineUUIDs() {
		if needHidden && hiddenMap[key] {
			continue
		}
		if needGroup && !groupMap[key] {
			continue
		}
		if targetUUID != "" && key != targetUUID {
			continue
		}
		resp.Online = append(resp.Online, key)
	}

	calibrated, _ := trafficledger.CurrentCalibratedCycleUsages(ctx, db, time.Now().UTC())
	for key, report := range agent_runtime.GetLatestReport() {
		if needHidden && hiddenMap[key] {
			continue
		}
		if needGroup && !groupMap[key] {
			continue
		}
		if targetUUID != "" && key != targetUUID {
			continue
		}

		report.UUID = "" // 不暴露 uuid
		if report.CPU.Usage == 0 {
			report.CPU.Usage = 0.01
		}
		if usage, ok := calibrated[key]; ok {
			report.Network.TotalUp = usage.Up
			report.Network.TotalDown = usage.Down
		}
		resp.Data[key] = *report
	}
	return resp
}

func getCachedWSClientsSnapshot(ctx context.Context, db *gorm.DB, isLogin bool, group string) ([]byte, error) {
	cacheGen := clients.CacheGeneration()
	key := wsClientsSnapshotKey{
		db:       db,
		cacheGen: cacheGen,
		isLogin:  isLogin,
		group:    group,
	}
	now := time.Now()

	wsSnapshotMu.RLock()
	if entry, ok := wsSnapshotCache[key]; ok && now.Before(entry.expires) {
		wsSnapshotMu.RUnlock()
		return entry.payload, nil
	}
	wsSnapshotMu.RUnlock()

	sfKey := fmt.Sprintf("%p|%d|%t|%s", db, cacheGen, isLogin, group)
	val, err, _ := wsSnapshotFlight.Do(sfKey, func() (any, error) {
		checkNow := time.Now()
		wsSnapshotMu.RLock()
		if entry, ok := wsSnapshotCache[key]; ok && checkNow.Before(entry.expires) {
			wsSnapshotMu.RUnlock()
			return entry.payload, nil
		}
		wsSnapshotMu.RUnlock()

		resp := buildWSClientsPayload(ctx, db, isLogin, group, "")
		encoded, marshalErr := json.Marshal(gin.H{"status": "success", "data": resp})
		if marshalErr != nil {
			return nil, marshalErr
		}

		wsSnapshotMu.Lock()
		if len(wsSnapshotCache) >= 32 {
			for k, v := range wsSnapshotCache {
				if !checkNow.Before(v.expires) {
					delete(wsSnapshotCache, k)
				}
			}
			if len(wsSnapshotCache) >= 32 {
				wsSnapshotCache = make(map[wsClientsSnapshotKey]wsClientsSnapshotEntry)
			}
		}
		wsSnapshotCache[key] = wsClientsSnapshotEntry{
			payload: encoded,
			expires: time.Now().Add(wsClientsSnapshotTTL),
		}
		wsSnapshotMu.Unlock()
		return encoded, nil
	})
	if err != nil {
		return nil, err
	}
	return val.([]byte), nil
}

func GetClients(c *gin.Context) {
	// 升级到ws
	if !IsWebSocketUpgrade(c) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Require WebSocket upgrade"})
		return
	}
	// Upgrade the HTTP connection to a WebSocket connection
	conn, err := UpgradeWebSocket(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Failed to upgrade to WebSocket." + err.Error()})
		return
	}
	defer conn.Close()

	// 初始化用户信息
	session, _ := c.Cookie("session_token")
	isLogin := false
	if _, err = accounts.GetUserBySession(session); err == nil {
		isLogin = true
	}
	group := strings.TrimSpace(c.Query("group"))

	// 请求
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		message := string(data)

		if message == "get" {
			payload, snapErr := getCachedWSClientsSnapshot(c.Request.Context(), dbcore.GetDBInstance(), isLogin, group)
			if snapErr != nil {
				if conn.WriteJSON(gin.H{"status": "error", "error": snapErr.Error()}) != nil {
					return
				}
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
			continue
		}

		if !strings.HasPrefix(message, "get ") {
			if conn.WriteJSON(gin.H{"status": "error", "error": "Invalid message"}) != nil {
				return
			}
			continue
		}
		uuID := strings.TrimSpace(strings.TrimPrefix(message, "get "))
		resp := buildWSClientsPayload(c.Request.Context(), dbcore.GetDBInstance(), isLogin, group, uuID)
		if err := conn.WriteJSON(gin.H{"status": "success", "data": resp}); err != nil {
			return
		}
	}
}
