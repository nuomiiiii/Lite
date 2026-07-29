package outboundhttp

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
)

func TestIPv6FirstDialContextPrefersIPv6AndFallsBackToIPv4(t *testing.T) {
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("192.0.2.1")},
			{IP: net.ParseIP("2001:db8::1")},
		}, nil
	}
	var attempted []string
	dial := func(_ context.Context, _ string, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		return nil, errors.New("unreachable")
	}
	_, err := ipv6FirstDialContext(lookup, dial)(context.Background(), "tcp", "notify.example:443")
	if err == nil {
		t.Fatal("dial succeeded unexpectedly")
	}
	want := []string{"[2001:db8::1]:443", "192.0.2.1:443"}
	if !reflect.DeepEqual(attempted, want) {
		t.Fatalf("dial order = %v, want %v", attempted, want)
	}
}
