package rpc

import "strings"

// Unregister 注销已注册的方法，并清理其元数据。
// 保留前缀 "rpc." 的内部方法禁止注销。返回是否存在并被移除。
// 主要供插件卸载时动态移除其注册的方法。
func Unregister(method string) bool {
	method = strings.TrimSpace(method)
	if method == "" || strings.HasPrefix(method, "rpc.") {
		return false
	}
	muHandlers.Lock()
	_, exists := handlers[method]
	if exists {
		delete(handlers, method)
	}
	muHandlers.Unlock()
	if exists {
		muMetas.Lock()
		delete(methodMetas, method)
		muMetas.Unlock()
	}
	return exists
}
