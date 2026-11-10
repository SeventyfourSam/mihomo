package dns

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/hook"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	icontext "github.com/metacubex/mihomo/context"
	D "github.com/miekg/dns"
)

func TestDNSHookWaitsBeforeReplyAndSkipsFakeIP(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	cfg, err := hook.Parse(map[string]hook.ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL}}, []hook.RuleConfig{{Event: "IP", Match: "DOMAIN,example.test", Providers: []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook.Apply(cfg)
	defer hook.Shutdown()
	var fake atomic.Bool
	service := &Service{handler: withHooks(func(ctx *icontext.DNSContext, req *D.Msg) (*D.Msg, error) {
		if fake.Load() {
			ctx.SetType(icontext.DNSTypeFakeIP)
		}
		response := new(D.Msg)
		response.SetReply(req)
		response.Answer = []D.RR{&D.A{Hdr: D.RR_Header{Name: "example.test.", Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 1)}}
		return response, nil
	})}
	request := new(D.Msg)
	request.SetQuestion("example.test.", D.TypeA)
	done := make(chan error, 1)
	go func() { _, err := service.ServeMsg(context.Background(), request); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("IP hook not called")
	}
	select {
	case <-done:
		t.Fatal("DNS replied before synchronous hook")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DNS did not finish")
	}
	fake.Store(true)
	if _, err := service.ServeMsg(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("Fake-IP reported as a real target")
	default:
	}
}

func TestDNSHostRejectionPreventsResolution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	cfg, err := hook.Parse(map[string]hook.ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL}}, []hook.RuleConfig{{Event: "HOST", Match: "MATCH", Providers: []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook.Apply(cfg)
	defer hook.Shutdown()
	resolved := false
	service := &Service{handler: withHooks(func(*icontext.DNSContext, *D.Msg) (*D.Msg, error) { resolved = true; return nil, nil })}
	request := new(D.Msg)
	request.SetQuestion("example.test.", D.TypeA)
	if _, err := service.ServeMsg(context.Background(), request); err == nil || resolved {
		t.Fatal("blocked HOST still resolved", err)
	}
}

type hookDNSClient struct{ calls atomic.Int32 }

func (*hookDNSClient) Address() string  { return "hook-test" }
func (*hookDNSClient) ResetConnection() {}
func (c *hookDNSClient) ExchangeContext(ctx context.Context, req *D.Msg) (*D.Msg, error) {
	c.calls.Add(1)
	m := new(D.Msg)
	m.SetReply(req)
	m.Answer = []D.RR{&D.A{Hdr: D.RR_Header{Name: req.Question[0].Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 300}, A: net.IPv4(192, 0, 2, 1)}}
	return m, nil
}

func TestRealDNSCacheAndHostsStillRunHooks(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	cfg, err := hook.Parse(map[string]hook.ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL}}, []hook.RuleConfig{{Event: "IP", Match: "MATCH", Providers: []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hook.Apply(cfg)
	defer hook.Shutdown()
	client := &hookDNSClient{}
	r := NewResolverFromClient(client)
	service := NewService(r, &ResolverEnhancer{ipv6: true})
	request := new(D.Msg)
	request.SetQuestion("cached.example.test.", D.TypeA)
	for i := 0; i < 2; i++ {
		if _, err := service.ServeMsg(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if client.calls.Load() != 1 || calls.Load() != 2 {
		t.Fatal("cache hit skipped hook", client.calls.Load(), calls.Load())
	}
	oldHosts := resolver.DefaultHosts
	defer func() { resolver.DefaultHosts = oldHosts }()
	tree := trie.New[resolver.HostValue]()
	value, err := resolver.NewHostValueByIPs([]netip.Addr{netip.MustParseAddr("192.0.2.2")})
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Insert("hosts.example.test", value); err != nil {
		t.Fatal(err)
	}
	resolver.DefaultHosts = resolver.NewHosts(tree)
	service = NewService(r, &ResolverEnhancer{ipv6: true, useHosts: true})
	request.SetQuestion("hosts.example.test.", D.TypeA)
	if _, err := service.ServeMsg(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if client.calls.Load() != 1 || calls.Load() != 3 {
		t.Fatal("hosts skipped hook or queried DNS", client.calls.Load(), calls.Load())
	}
}
