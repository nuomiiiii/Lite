package tcplisten

import (
	"context"
	"net"
)

func newListenConfig() net.ListenConfig {
	var listenConfig net.ListenConfig
	listenConfig.SetMultipathTCP(false)
	return listenConfig
}

// Listen accepts TCP connections without Multipath TCP.
// Go 1.24+ enables MPTCP on Linux listeners by default; SetMultipathTCP(false)
// keeps this socket on plain TCP even if GODEBUG re-enables the default.
func Listen(network, address string) (net.Listener, error) {
	listenConfig := newListenConfig()
	return listenConfig.Listen(context.Background(), network, address)
}
