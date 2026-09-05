package client

import (
	"sync"
	"time"

	"github.com/nuomiiiii/lite/utils/notifier"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
)

const (
	postPresenceTTL = 35 * time.Second
)

// postPresenceEntry 保存单个客户端的 POST 上报会话状态
type postPresenceEntry struct {
	connID     int64
	timer      *time.Timer
	generation uint64 // 每次 Reset 递增，用于回调中判断是否为过期的旧回调
}

var (
	postPresenceMu     sync.Mutex
	postPresenceStates = make(map[string]*postPresenceEntry)
)

// refreshPostPresence 管理 HTTP POST 上报者的在线/离线状态。
// 每次 POST 刷新 TTL 定时器；定时器到期后触发离线通知。
// 协议版本不再写在这里：v1 已移除，在线只看 WS 连接或这条 presence TTL。
func refreshPostPresence(uuid string) {
	postPresenceMu.Lock()
	defer postPresenceMu.Unlock()

	if entry, exists := postPresenceStates[uuid]; exists {
		// 已在线：递增 generation 使可能正在执行的旧回调失效
		entry.generation++
		entry.timer.Stop()
		// 重新创建 AfterFunc 以在闭包中捕获新的 generation
		gen := entry.generation
		entry.timer = time.AfterFunc(postPresenceTTL, func() {
			postPresenceExpired(uuid, entry.connID, gen)
		})
		agent_runtime.KeepAlivePresence(uuid, entry.connID, postPresenceTTL)
		return
	}

	// 新 POST 会话：生成 connID，标记在线，启动超时定时器
	connID := time.Now().UnixNano()
	agent_runtime.KeepAlivePresence(uuid, connID, postPresenceTTL)
	go notifier.OnlineNotification(uuid, connID)

	defaultGeneration := uint64(0)

	entry := &postPresenceEntry{
		connID:     connID,
		generation: defaultGeneration,
	}

	entry.timer = time.AfterFunc(postPresenceTTL, func() {
		postPresenceExpired(uuid, connID, defaultGeneration)
	})

	postPresenceStates[uuid] = entry
}

// postPresenceExpired 是定时器到期的回调。
// 只有当 connID 和 generation 都与当前 entry 匹配时才执行离线清理，
// 避免 timer.Reset 竞态导致过期回调错误地清除仍活跃的会话。
func postPresenceExpired(uuid string, connID int64, gen uint64) {
	postPresenceMu.Lock()
	e, ok := postPresenceStates[uuid]
	if !ok || e.connID != connID || e.generation != gen {
		postPresenceMu.Unlock()
		return
	}
	delete(postPresenceStates, uuid)
	postPresenceMu.Unlock()

	agent_runtime.SetPresence(uuid, connID, false)
	notifier.OfflineNotification(uuid, connID)
}
