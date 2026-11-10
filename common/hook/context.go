// Package hook carries optional business-target callbacks through low-level I/O.
// It deliberately depends only on the standard library.
package hook

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
)

type Observer interface {
	Lookup(context.Context, string, []netip.Addr) error
	BeforeDial(context.Context, netip.Addr, string) error
}

type binding struct {
	observer Observer
	address  string
}
type contextKey struct{}

func WithObserver(ctx context.Context, observer Observer, address string) context.Context {
	return context.WithValue(ctx, contextKey{}, binding{observer, address})
}

func Without(ctx context.Context) context.Context {
	if ObserverFrom(ctx) == nil {
		return ctx
	}
	return WithObserver(ctx, nil, "")
}

func ObserverFrom(ctx context.Context) Observer {
	b, _ := ctx.Value(contextKey{}).(binding)
	return b.observer
}

// ForDial suppresses callbacks for transport endpoints such as proxy servers.
func ForDial(ctx context.Context, address string) context.Context {
	b, _ := ctx.Value(contextKey{}).(binding)
	if b.observer == nil {
		return ctx
	}
	if b.address == "" || !sameAddress(b.address, address) {
		return Without(ctx)
	}
	return ctx
}

func sameAddress(a, b string) bool {
	ah, ap, ae := net.SplitHostPort(a)
	bh, bp, be := net.SplitHostPort(b)
	return ae == nil && be == nil && ap == bp && strings.EqualFold(strings.TrimRight(ah, "."), strings.TrimRight(bh, "."))
}

// LookupResult is used as a defer around a public resolver operation.
func LookupResult(ctx context.Context, host string, ips *[]netip.Addr, err *error) {
	if *err != nil || len(*ips) == 0 {
		return
	}
	if observer := ObserverFrom(ctx); observer != nil {
		if hookErr := observer.Lookup(ctx, host, *ips); hookErr != nil {
			*ips, *err = nil, hookErr
		}
	}
}

func BeforeDial(ctx context.Context, ip netip.Addr, port string) error {
	if observer := ObserverFrom(ctx); observer != nil {
		return observer.BeforeDial(ctx, ip, port)
	}
	return nil
}

// ErrBlocked stays distinguishable through DNS, adapter and dialer error wraps.
var ErrBlocked = errors.New("hook blocked operation")

func IsBlocked(err error) bool { return errors.Is(err, ErrBlocked) }
