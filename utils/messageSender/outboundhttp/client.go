// Package outboundhttp provides the HTTP client used by configurable notification senders.
package outboundhttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t.DialContext = ipv6FirstDialContext(net.DefaultResolver.LookupIPAddr, dialer.DialContext)
	return t
}()

// Keep IPv6 preferred without letting an unreachable IPv6 path block notifications.
const ipv4FallbackDelay = 300 * time.Millisecond

// NewClient prefers IPv6 for dual-stack endpoints and falls back to IPv4.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: transport, Timeout: timeout}
}

func ipv6FirstDialContext(
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	return ipv6FirstDialContextWithDelay(lookup, dial, ipv4FallbackDelay)
}

type dialResult struct {
	conn net.Conn
	err  error
}

func ipv6FirstDialContextWithDelay(
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
	fallbackDelay time.Duration,
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return dial(ctx, network, address)
		}
		addresses, err := lookup(ctx, host)
		if err != nil || len(addresses) == 0 {
			return dial(ctx, network, address)
		}

		var ipv6, ipv4 []net.IPAddr
		for _, ipAddr := range addresses {
			if ipAddr.IP.To4() == nil {
				ipv6 = append(ipv6, ipAddr)
			} else {
				ipv4 = append(ipv4, ipAddr)
			}
		}
		if strings.HasSuffix(network, "4") {
			ipv6 = nil
		}
		if strings.HasSuffix(network, "6") {
			ipv4 = nil
		}

		if len(ipv6) == 0 || len(ipv4) == 0 {
			return dialAddresses(ctx, network, port, append(ipv6, ipv4...), dial)
		}

		raceCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		results := make(chan dialResult)
		start := func(candidates []net.IPAddr) {
			go func() {
				conn, err := dialAddresses(raceCtx, network, port, candidates, dial)
				select {
				case results <- dialResult{conn: conn, err: err}:
				case <-raceCtx.Done():
					if conn != nil {
						_ = conn.Close()
					}
				}
			}()
		}

		start(ipv6)
		timer := time.NewTimer(fallbackDelay)
		defer timer.Stop()
		ipv4Started := false
		pending := 1
		var errs []error

		for pending > 0 {
			select {
			case result := <-results:
				pending--
				if result.conn != nil {
					return result.conn, nil
				}
				if result.err != nil {
					errs = append(errs, result.err)
				}
				if !ipv4Started {
					ipv4Started = true
					pending++
					start(ipv4)
				}
			case <-timer.C:
				if !ipv4Started {
					ipv4Started = true
					pending++
					start(ipv4)
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		return nil, errors.Join(errs...)
	}
}

func dialAddresses(
	ctx context.Context,
	network, port string,
	addresses []net.IPAddr,
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	if len(addresses) == 0 {
		return nil, errors.New("no suitable IP address for requested network")
	}
	var errs []error
	for _, ipAddr := range addresses {
		conn, err := dial(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}
