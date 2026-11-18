package notifier

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logger "github.com/nuomiiiii/lite/utils/log"

	"github.com/nuomiiiii/lite/database/alertsilence"
	"github.com/nuomiiiii/lite/database/clients"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	messageevent "github.com/nuomiiiii/lite/database/models/messageEvent"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils/messageSender"
	"github.com/nuomiiiii/lite/utils/renewal"
)

// notificationState 保存单个客户端的通知状态。
// 通过在结构体中嵌入互斥锁，实现每个客户端细粒度的锁定，比全局锁更高效。
type notificationState struct {
	mu                  sync.Mutex // 互斥锁，保护该客户端状态
	pendingOfflineSince time.Time  // 客户端离线的时间。为零值表示客户端在线或已发送离线通知。
	isFirstConnection   bool       // 标记是否为首次上线连接。
	isConnExist         bool       // 标记是否存在连接
	connectionID        int64      // 连接ID，用于区分不同的连接会话，防止竞态条件
}

// clientStates 使用 sync.Map 实现对客户端状态的并发访问。
// 映射关系：clientID (string) -> *notificationState
var clientStates sync.Map

var suppressOffline atomic.Bool

// ClientIsLive reports whether the node is currently reachable on any
// transport. The HTTP/WS layer sets this so a dead WebSocket does not page
// operators while HTTP reports are still landing, and so panel shutdown does
// not flush a fleet-wide offline burst.
var ClientIsLive func(clientID string) bool

// SuppressOfflineNotifications drops disconnect events. Call this before
// closing agent sockets on panel shutdown; those closes are the panel going
// away, not the nodes.
func SuppressOfflineNotifications() {
	suppressOffline.Store(true)
}

// ResumeOfflineNotifications re-enables disconnect events when starting or
// restarting background services in-process.
func ResumeOfflineNotifications() {
	suppressOffline.Store(false)
}

func skipOfflineSend(clientID string) bool {
	if suppressOffline.Load() {
		return true
	}
	if ClientIsLive != nil && ClientIsLive(clientID) {
		return true
	}
	return false
}

func ForgetClient(clientID string) {
	value, exists := clientStates.LoadAndDelete(clientID)
	if !exists {
		return
	}
	state := value.(*notificationState)
	state.mu.Lock()
	state.pendingOfflineSince = time.Time{}
	state.isConnExist = false
	state.connectionID++
	state.mu.Unlock()
}

// getNotificationConfig 获取指定客户端的通知配置。
// 返回配置对象和一个布尔值，指示全局和该客户端是否启用通知。
func getNotificationConfig(clientID string) (*models.OfflineNotification, bool) {
	conf, err := config.GetAs[bool](config.NotificationEnabledKey, false)
	if err != nil || !conf {
		return nil, false
	}

	notiConf := models.OfflineNotification{Client: clientID}
	db := dbcore.GetDBInstance()
	if err := db.Model(&models.OfflineNotification{}).Where("client = ?", clientID).FirstOrCreate(&notiConf).Error; err != nil {
		logger.Errorf("notifier", "Failed to get or create offline notification config for client %s: %v", clientID, err)
		return nil, false
	}

	return &notiConf, notiConf.Enable
}

// getOrInitState 从 sync.Map 获取客户端状态，不存在则新建并存储。
func getOrInitState(clientID string) *notificationState {
	// 原子性地加载或存储该客户端的状态。
	val, _ := clientStates.LoadOrStore(clientID, &notificationState{isFirstConnection: true})
	return val.(*notificationState)
}

func beginOfflineGrace(state *notificationState, endedConnectionID int64, now time.Time) bool {
	state.mu.Lock()
	defer state.mu.Unlock()

	// 只接受当前连接的首次离线事件。旧连接的延迟事件必须被忽略。
	if !state.pendingOfflineSince.IsZero() || state.connectionID != endedConnectionID {
		return false
	}
	state.pendingOfflineSince = now
	return true
}

func recordOnlineConnection(state *notificationState, connectionID int64) (notifyOnline, duplicate bool) {
	state.mu.Lock()
	defer state.mu.Unlock()

	state.connectionID = connectionID
	if state.isFirstConnection {
		state.isFirstConnection = false
		state.pendingOfflineSince = time.Time{}
		state.isConnExist = true
		return false, false
	}

	wasPending := !state.pendingOfflineSince.IsZero()
	state.pendingOfflineSince = time.Time{}
	if wasPending {
		return false, false
	}

	if state.isConnExist {
		return false, true
	}
	state.isConnExist = true
	return true, false
}

// RebindOnlineConnection hands notification ownership to a surviving transport
// when another session for the same client closes, so a later real disconnect
// of the surviving session still matches state.connectionID.
func RebindOnlineConnection(clientID string, endedConnectionID, survivingConnectionID int64) {
	if clientID == "" {
		return
	}
	val, exists := clientStates.Load(clientID)
	if !exists {
		return
	}
	state := val.(*notificationState)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.connectionID == endedConnectionID || !state.isConnExist {
		state.connectionID = survivingConnectionID
		state.pendingOfflineSince = time.Time{}
		state.isConnExist = true
	}
}

// OfflineNotification 在启用通知且未在宽限期内发送的情况下，发送客户端离线通知。
func OfflineNotification(clientID string, endedConnectionID int64) {
	if skipOfflineSend(clientID) {
		return
	}
	client, err := clients.GetClientByUUID(clientID)
	if err != nil {
		return
	}

	notiConf, enabled := getNotificationConfig(clientID)
	if !enabled {
		return
	}

	gracePeriod := time.Duration(notiConf.GracePeriod) * time.Second
	if gracePeriod <= 0 {
		gracePeriod = 5 * time.Minute // 默认宽限期
	}

	now := time.Now().UTC()
	state := getOrInitState(clientID)
	if !beginOfflineGrace(state, endedConnectionID, now) {
		return
	}

	// 新建协程，等待宽限期后判断是否需要发送通知。
	go func(startTime time.Time, expectedConnectionID int64) {
		time.Sleep(gracePeriod)

		state.mu.Lock()
		defer state.mu.Unlock()

		// 检查离线状态是否仍为本次协程启动时的状态。
		// 若为零值，说明客户端已重连。
		// 当前的 connectionID 是否还是我们触发离线时的那个ID。如果不是，说明客户端重连过，本次离线通知已失效。
		if state.pendingOfflineSince.IsZero() || state.connectionID != expectedConnectionID {
			logger.Infof("notifier", "%s is reconnected new connID: %d, old connID: %d", clientID, state.connectionID, expectedConnectionID)
			return
		}
		if skipOfflineSend(clientID) {
			state.pendingOfflineSince = time.Time{}
			return
		}

		// 即将发送通知，重置待通知状态。
		// 需要多一个boolean 是因为pendingOfflineSince在offline睡眠后才修改，可能导致online判断不对
		state.pendingOfflineSince = time.Time{}
		state.isConnExist = false

		// Send notification
		message := fmt.Sprintf("🔴%s is offline", client.Name)
		go func(msg string) {
			if alertsilence.IsSilencedWithDB(dbcore.GetDBInstance(), alertsilence.KindOffline, clientID, time.Now().UTC()) {
				return
			}
			if err := messageSender.SendEvent(models.EventMessage{
				Kind:    messageSender.KindOffline,
				Event:   messageevent.Offline,
				Clients: []models.Client{client},
				Time:    time.Now().UTC(),
				//Message: msg,
				Emoji: "🔴",
			}); err != nil {
				logger.ErrorArgs("notifier", "Failed to send offline notification:", err)
			}
		}(message)

		// 更新数据库中的最后通知时间
		db := dbcore.GetDBInstance()
		if err := db.Model(&models.OfflineNotification{}).Where("client = ?", clientID).Update("last_notified", now.UTC()).Error; err != nil {
			logger.Errorf("notifier", "Failed to update last_notified for client %s: %v", clientID, err)
		}
	}(now, endedConnectionID)
}

// NotifyOnlineAsync synchronously records the online connection ID in memory
// (preventing instant-disconnect races where Offline runs before Online) and
// runs DB renewal/notification checks asynchronously.
func NotifyOnlineAsync(clientID string, connectionID int64) {
	if clientID == "" {
		return
	}
	state := getOrInitState(clientID)
	notifyOnline, duplicate := recordOnlineConnection(state, connectionID)
	go func() {
		defer func() { _ = recover() }()
		finishOnlineNotification(clientID, connectionID, notifyOnline, duplicate)
	}()
}

// OnlineNotification 在启用通知的情况下，发送客户端上线通知。
func OnlineNotification(clientID string, connectionID int64) {
	if clientID == "" {
		return
	}
	state := getOrInitState(clientID)
	notifyOnline, duplicate := recordOnlineConnection(state, connectionID)
	finishOnlineNotification(clientID, connectionID, notifyOnline, duplicate)
}

func finishOnlineNotification(clientID string, connectionID int64, notifyOnline, duplicate bool) {
	client, err := clients.GetClientByUUID(clientID)
	if err != nil {
		return
	}
	// 上线时检测续费
	renewal.CheckAndAutoRenewal(client)

	notiConf, enabled := getNotificationConfig(clientID)
	if !enabled {
		return
	}
	if duplicate {
		logger.Infof("notifier", "%s has connection exist: %d", clientID, connectionID)
		return
	}
	if !notifyOnline {
		return
	}

	// 规则4：客户端离线足够久已通知（或未待离线），现在重新上线，发送上线通知。
	now := time.Now().UTC()
	message := onlineRecoveryMessageFrom(notiConf, now)
	go func(msg string) {
		if err := messageSender.SendEvent(models.EventMessage{
			Kind:    messageSender.KindOnline,
			Event:   messageevent.Online,
			Clients: []models.Client{client},
			Time:    now,
			Message: msg,
			Emoji:   "🟢",
		}); err != nil {
			logger.ErrorArgs("notifier", "Failed to send online notification:", err)
		}
	}(message)
}

func onlineRecoveryMessageFrom(conf *models.OfflineNotification, now time.Time) string {
	if conf == nil || conf.LastNotified == nil || conf.LastNotified.IsZero() {
		return ""
	}
	return onlineRecoveryMessage(*conf.LastNotified, now)
}

func onlineRecoveryMessage(offlineSince, now time.Time) string {
	if offlineSince.IsZero() || now.Before(offlineSince) {
		return ""
	}
	return "离线时间：" + formatChineseDuration(now.Sub(offlineSince))
}

func formatChineseDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int64(d.Round(time.Second).Seconds())
	if total < 0 {
		total = 0
	}
	hours := total / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60
	var b strings.Builder
	if hours > 0 {
		fmt.Fprintf(&b, "%d小时", hours)
	}
	if minutes > 0 {
		fmt.Fprintf(&b, "%d分钟", minutes)
	}
	if seconds > 0 || b.Len() == 0 {
		fmt.Fprintf(&b, "%d秒", seconds)
	}
	return b.String()
}
