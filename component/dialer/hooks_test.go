package dialer

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"

	bridge "github.com/metacubex/mihomo/common/hook"
	"github.com/metacubex/mihomo/component/resolver"
)

type hookObserver struct {
	lookups atomic.Int32
	dials   atomic.Int32
	deny    bool
}

func (o *hookObserver) Lookup(context.Context, string, []netip.Addr) error {
	o.lookups.Add(1)
	return nil
}
func (o *hookObserver) BeforeDial(context.Context, netip.Addr, string) error {
	o.dials.Add(1)
	if o.deny {
		return fmt.Errorf("%w: test rejection", bridge.ErrBlocked)
	}
	return nil
}

type hookResolver struct{ resolver.Resolver }

func (hookResolver) Invalid() bool { return true }
func (hookResolver) LookupIP(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")}, nil
}
func (r hookResolver) LookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return r.LookupIP(ctx, host)
}

func TestTargetHookPrecedesEveryActualDial(t *testing.T) {
	previous := GetTcpConcurrent()
	defer SetTcpConcurrent(previous)
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprint(concurrent), func(t *testing.T) {
			SetTcpConcurrent(concurrent)
			observer := &hookObserver{deny: true}
			var sockets atomic.Int32
			netDialer := NetDialerFunc(func(context.Context, string, string) (net.Conn, error) {
				sockets.Add(1)
				return nil, fmt.Errorf("test socket was reached")
			})
			ctx := bridge.WithObserver(context.Background(), observer, "target.test:443")
			_, err := DialContext(ctx, "tcp4", "target.test:443", WithResolver(hookResolver{}), WithNetDialer(netDialer))
			if !bridge.IsBlocked(err) || sockets.Load() != 0 || observer.dials.Load() != 2 || observer.lookups.Load() == 0 {
				t.Fatal("rejected target reached a socket", err, sockets.Load(), observer.dials.Load(), observer.lookups.Load())
			}
		})
	}
}

func TestTransportDialDoesNotInheritTargetHook(t *testing.T) {
	observer := &hookObserver{deny: true}
	var sockets atomic.Int32
	netDialer := NetDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		sockets.Add(1)
		return nil, fmt.Errorf("test socket")
	})
	ctx := bridge.WithObserver(context.Background(), observer, "target.test:443")
	_, _ = DialContext(ctx, "tcp4", "192.0.2.100:443", WithNetDialer(netDialer))
	if observer.lookups.Load() != 0 || observer.dials.Load() != 0 || sockets.Load() != 1 {
		t.Fatal("proxy transport was misclassified as business target")
	}
}
