package ws

import (
	"github.com/akizon77/komari/common"
	"github.com/gorilla/websocket"
)

var (
	ConnectedClients = make(map[string]*websocket.Conn)
	ConnectedUsers   = []*websocket.Conn{}
	LatestReport     = make(map[string]*common.Report)
)
