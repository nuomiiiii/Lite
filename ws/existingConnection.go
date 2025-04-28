package ws

import (
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/common"
)

var (
	ConnectedClients = make(map[string]*websocket.Conn)
	ConnectedUsers   = []*websocket.Conn{}
	LatestReport     = make(map[string]*common.Report)
)
