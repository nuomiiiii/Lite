package jsonRpc

import (
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/config"
	"github.com/komari-monitor/komari/utils/rpc"
	"github.com/komari-monitor/komari/ws"
)

// Json Rpc2 over websocket, /api/rpc2
func OnRpcRequest(c *gin.Context) {
	cfg, _ := config.Get()
	// Upgrade
	_conn, err := ws.UpgradeRequest(c, func(r *http.Request) bool {
		if cfg.AllowCors {
			return true
		}
		return ws.CheckOrigin(r)
	})
	conn := ws.NewSafeConn(_conn)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "Failed to upgrade to WebSocket"})
		return
	}
	// 权限
	permissionGroup := "guest"

	// 节点端
	token := c.Query("Authorization")
	_, err = clients.GetClientUUIDByToken(token)
	if err == nil {
		permissionGroup = "client"
	}
	// 管理员
	session_token, _ := c.Cookie("session_token")
	_, err = accounts.GetUserBySession(session_token)
	if err == nil {
		permissionGroup = "admin"
	}
	apiKey := c.GetHeader("Authorization")
	if apiKey == "Bearer "+cfg.ApiKey {
		permissionGroup = "admin"
	}

	defer conn.Close()
	for {
		// 基础行为检查
		var req rpc.JsonRpcRequest
		err := conn.ReadJSON(&req)
		if err != nil {
			conn.WriteJSON(rpc.ErrorResponse(nil, rpc.InvalidRequest, "bad request: "+err.Error(), nil))
			continue
		}
		if jerr := req.Validate(); jerr != nil {
			conn.WriteJSON(jerr.ResponseWithID(req.ID))
			continue
		}
		// 按分组权限检查并调用
		fc := strings.Split(req.Method, ":")
		if len(fc) == 1 {
			fc[0] = "common"
		}
		switch fc[0] {
		case "guest":
			fallthrough
		case "":
			fallthrough
		case "rpc":
			fallthrough
		case "common":
			go conn.WriteJSON(rpc.Call(req.ID, req.Method, req.Params))
		case "client":
			if permissionGroup == "client" || permissionGroup == "admin" {
				go conn.WriteJSON(rpc.Call(req.ID, req.Method, req.Params))
			}
		case "admin":
			if permissionGroup == "admin" {
				go conn.WriteJSON(rpc.Call(req.ID, req.Method, req.Params))
			}
		default:
			conn.WriteJSON(rpc.ErrorResponse(req.ID, 401, "Unauthorized", nil))
		}
	}
}

// registry holds method handlers keyed by "namespace:MethodName"
var (
	registryMu sync.RWMutex
	registry   = make(map[string]reflect.Value)
)

// Register 将以分组common注册
func Register(name string, cb rpc.Handler) error {
	return RegisterWithGroupAndMeta(name, "common", cb, &rpc.MethodMeta{
		Name:        name,
		Summary:     "This method does not provide a summary",
		Description: "This method does not provide a description",
	})
}

// RegisterWithGroup 将回调按分组注册。
// 支持两种形式:
// 1) 传入函数:       Register("ns", someFunc) -> 注册 ns:SomeFunc
// 2) 传入结构体指针: Register("ns", &MyService{}) -> 为其所有导出方法注册 ns:Method
// group 为空时使用默认分组 "common"。
func RegisterWithGroupAndMeta(name, group string, cb rpc.Handler, meta *rpc.MethodMeta) error {
	return rpc.RegisterWithMeta(group+":"+name, cb, meta)
}

// GetRegisteredKeys 返回已注册的方法 key 列表 (调试用)。
func GetRegisteredKeys() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	keys := make([]string, 0, len(registry))
	for k := range registry {
		keys = append(keys, k)
	}
	return keys
}
