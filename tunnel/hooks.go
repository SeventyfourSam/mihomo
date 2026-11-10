package tunnel

import (
	"context"
	"net"
	"net/netip"

	"github.com/metacubex/mihomo/component/hook"
	"github.com/metacubex/mihomo/component/process"
	C "github.com/metacubex/mihomo/constant"
)

func resolveMetadataWithHooks(metadata *C.Metadata) (C.Proxy, C.Rule, context.Context, error) {
	s := hook.NewSession(metadata, FindProcessMode() != process.FindProcessOff)
	ctx := s.Context(context.Background())
	if err := s.Host(ctx); err != nil {
		return nil, nil, ctx, err
	}
	s.Collect()
	proxy, rule, err := resolveMetadata(ctx, metadata)
	if err == nil {
		err = s.FinishRoute(ctx, metadata, proxy.Name())
	}
	return proxy, rule, ctx, err
}

// The wrapper keeps hook state without changing PacketSender/NAT interfaces.
// It is installed even when disabled so existing UDP associations can pick up
// hooks after a configuration reload. State is allocated only when needed.
type hookPacketConn struct {
	C.PacketConn
	metadata  C.Metadata
	originKey string
	initial   *hook.Session
	sessions  map[udpHookKey]*hook.Session
}

type udpHookKey struct {
	origin string
	target netip.AddrPort
}

func withUDPHooks(pc C.PacketConn, metadata, origin *C.Metadata, ctx context.Context) C.PacketConn {
	return &hookPacketConn{PacketConn: pc, metadata: *metadata, originKey: origin.String(), initial: hook.FromContext(ctx)}
}

func (c *hookPacketConn) packetSession(metadata *C.Metadata, target netip.AddrPort) *hook.Session {
	if !hook.Enabled() {
		return nil
	}
	key := udpHookKey{metadata.String(), target}
	if s := c.sessions[key]; s.Current() {
		return s
	}
	m := c.metadata
	if key.origin != c.originKey {
		copyM := metadata.Clone()
		_ = preHandleMetadata(copyM) // restores mapping only; never resolves DNS
		m.Host, m.SniffHost, m.DNSMode = copyM.Host, copyM.SniffHost, copyM.DNSMode
	}
	m.DstIP, m.DstPort = target.Addr(), target.Port()
	s := c.initial
	if key.origin != c.originKey || target.Port() != c.metadata.DstPort || !s.Current() {
		s = hook.NewSession(&m, FindProcessMode() != process.FindProcessOff)
	}
	if c.sessions == nil || len(c.sessions) > 256 {
		c.sessions = make(map[udpHookKey]*hook.Session)
	}
	c.sessions[key] = s
	return s
}

func (c *hookPacketConn) ResolveUDP(ctx context.Context, metadata *C.Metadata) error {
	s := c.packetSession(metadata, metadata.AddrPort())
	if s != nil {
		s.RetryPacket()
		if err := s.Host(ctx); err != nil {
			return err
		}
		ctx = s.Context(ctx)
	}
	return c.PacketConn.ResolveUDP(ctx, metadata)
}

func beforeUDPHook(ctx context.Context, pc C.PacketConn, metadata *C.Metadata, addr *net.UDPAddr) error {
	c, ok := pc.(*hookPacketConn)
	if !ok || addr == nil {
		return nil
	}
	s := c.packetSession(metadata, addr.AddrPort())
	if s == nil {
		return nil
	}
	s.RetryPacket()
	if err := s.Host(ctx); err != nil {
		return err
	}
	return s.IPs(ctx, []netip.Addr{addr.AddrPort().Addr()}, "connection", uint16(addr.Port))
}
