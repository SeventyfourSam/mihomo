package tunnel

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	bridge "github.com/metacubex/mihomo/common/hook"
	"github.com/metacubex/mihomo/component/hook"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	RC "github.com/metacubex/mihomo/rules/common"
)

type hookTestPacket struct{ drops int }

func (*hookTestPacket) Data() []byte                            { return []byte("business packet") }
func (*hookTestPacket) WriteBack([]byte, net.Addr) (int, error) { return 0, nil }
func (*hookTestPacket) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}
func (p *hookTestPacket) Drop() { p.drops++ }

type hookTestPacketConn struct {
	C.PacketConn
	writes int
}

func (c *hookTestPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.writes++
	return len(p), nil
}
func (*hookTestPacketConn) SetReadDeadline(time.Time) error { return nil }

func TestCachedUDPTargetStillRequiresSuccessfulHook(t *testing.T) {
	var status, calls atomic.Int32
	status.Store(503)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	cfg, err := hook.Parse(map[string]hook.ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL}}, []hook.RuleConfig{{Event: "IP", Match: "DOMAIN,example.test", Providers: []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook.Apply(cfg)
	defer hook.Shutdown()
	m := &C.Metadata{Host: "example.test", DstIP: netip.MustParseAddr("192.0.2.1"), DstPort: 443, NetWork: C.UDP}
	raw := &hookTestPacketConn{}
	pc := withUDPHooks(raw, m, m, context.Background())
	sender := newPacketSender().(*packetSender)
	defer sender.Close()
	sender.AddMapping(m, m) // every test packet takes the cached mapping branch
	send := func() {
		p := &hookTestPacket{}
		sender.processPacket(pc, C.NewPacketAdapter(p, m))
		if p.drops != 1 {
			t.Fatal("packet ownership was not released")
		}
	}
	send()
	if raw.writes != 0 || calls.Load() != 1 {
		t.Fatal("UDP sent before confirmation", raw.writes, calls.Load())
	}
	status.Store(200)
	send()
	send()
	if raw.writes != 2 || calls.Load() != 2 {
		t.Fatal("UDP confirmation was not reused", raw.writes, calls.Load())
	}
	hook.Apply(cfg)
	status.Store(503)
	send()
	if raw.writes != 2 || calls.Load() != 3 {
		t.Fatal("reload reused an old confirmation", raw.writes, calls.Load())
	}
}

func TestHookRejectionStopsTunnelRetry(t *testing.T) {
	calls := 0
	_, err := retry(context.Background(), func(context.Context) (int, error) {
		calls++
		return 0, bridge.ErrBlocked
	}, nil)
	if !bridge.IsBlocked(err) || calls != 1 {
		t.Fatal("hook rejection retried", err, calls)
	}
}

type hookTestProxy struct {
	C.Proxy
	name string
}

func (p hookTestProxy) Name() string                   { return p.name }
func (hookTestProxy) Type() C.AdapterType              { return C.Direct }
func (hookTestProxy) Unwrap(*C.Metadata, bool) C.Proxy { return nil }

func TestHooksApplyToEveryRoutingMode(t *testing.T) {
	oldMode, oldProxies := mode, proxies
	defer func() { mode, proxies = oldMode, oldProxies }()
	proxies = map[string]C.Proxy{"DIRECT": hookTestProxy{name: "DIRECT"}, "GLOBAL": hookTestProxy{name: "GLOBAL"}, "chosen": hookTestProxy{name: "chosen"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	cfg, err := hook.Parse(map[string]hook.ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL}}, []hook.RuleConfig{{Event: "IP", Match: "MATCH", Providers: []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook.Apply(cfg)
	defer hook.Shutdown()
	for _, tc := range []struct {
		mode    TunnelMode
		special string
	}{{Direct, ""}, {Global, ""}, {Rule, "chosen"}} {
		mode = tc.mode
		m := &C.Metadata{DstIP: netip.MustParseAddr("192.0.2.1"), DstPort: 443, NetWork: C.TCP, SpecialProxy: tc.special}
		_, _, _, err := resolveMetadataWithHooks(m)
		if !bridge.IsBlocked(err) {
			t.Fatal("routing shortcut bypassed IP hook", tc, err)
		}
	}
}

type hookTestResolver struct{ resolver.Resolver }

func (hookTestResolver) Invalid() bool { return true }
func (hookTestResolver) LookupIPv4(context.Context, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
}

func TestRoutingDNSHookDoesNotHoldConfigLock(t *testing.T) {
	oldMode, oldProxies, oldRules := mode, proxies, rules
	oldResolver, oldIPv6 := resolver.DefaultResolver, resolver.DisableIPv6
	defer func() {
		mode, proxies, rules = oldMode, oldProxies, oldRules
		resolver.DefaultResolver, resolver.DisableIPv6 = oldResolver, oldIPv6
	}()
	mode = Rule
	proxies = map[string]C.Proxy{"DIRECT": hookTestProxy{name: "DIRECT"}}
	ipRule, err := RC.NewIPCIDR("192.0.2.0/24", "DIRECT")
	if err != nil {
		t.Fatal(err)
	}
	rules = []C.Rule{ipRule}
	resolver.DefaultResolver, resolver.DisableIPv6 = hookTestResolver{}, true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		configMux.Lock()
		configMux.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()
	cfg, err := hook.Parse(map[string]hook.ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL, Timeout: time.Second}}, []hook.RuleConfig{{Event: "IP", Match: "MATCH", Providers: []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook.Apply(cfg)
	defer hook.Shutdown()
	m := &C.Metadata{Host: "example.test", DstPort: 443, NetWork: C.TCP}
	if _, _, _, err := resolveMetadataWithHooks(m); err != nil {
		t.Fatal("hook waited while routing lock was held", err)
	}
}
