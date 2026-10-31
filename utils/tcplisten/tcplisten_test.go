package tcplisten

import (
	"net"
	"testing"
)

func TestDefaultListenConfigDisablesMultipathTCP(t *testing.T) {
	var listenConfig net.ListenConfig
	if listenConfig.MultipathTCP() {
		t.Fatal("default ListenConfig still enables MPTCP; godebug multipathtcp=0 is missing from go.mod")
	}
}

func TestListenDisablesMultipathTCP(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=1")

	var enabled net.ListenConfig
	if !enabled.MultipathTCP() {
		t.Fatal("GODEBUG=multipathtcp=1 did not enable the listen default")
	}
	listenConfig := newListenConfig()
	if listenConfig.MultipathTCP() {
		t.Fatal("Listen still reports MPTCP enabled")
	}

	listener, err := Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	_ = listener.Close()
}
