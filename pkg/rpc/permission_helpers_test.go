package rpc

import "strings"

// NamespaceOf 解析方法名的命名空间。"ns:method" 返回 "ns"；无 ":" 返回 DefaultNamespace。
func NamespaceOf(method string) string {
	if i := strings.IndexByte(method, ':'); i >= 0 {
		return method[:i]
	}
	return DefaultNamespace
}
