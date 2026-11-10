package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"

	bridge "github.com/metacubex/mihomo/common/hook"
	"github.com/metacubex/mihomo/component/fakeip"
	"github.com/metacubex/mihomo/component/hook"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	D "github.com/miekg/dns"
)

func hookTestRecords(t *testing.T, records []string) []D.RR {
	t.Helper()
	var result []D.RR
	for _, text := range records {
		rr, err := D.NewRR(text)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, rr)
	}
	return result
}

func TestHookResponseIPs(t *testing.T) {
	for _, tc := range []struct {
		name                string
		answer, extra, want []string
	}{
		{"A-AAAA", []string{"example.test. 60 IN A 192.0.2.1", "example.test. 60 IN AAAA 2001:db8::1"}, nil, []string{"192.0.2.1", "2001:db8::1"}},
		{"HTTPS-hints", []string{"example.test. 60 IN HTTPS 1 . ipv4hint=192.0.2.1 ipv6hint=2001:db8::1"}, nil, []string{"192.0.2.1", "2001:db8::1"}},
		{"SVCB-hints", []string{"example.test. 60 IN SVCB 1 . ipv4hint=192.0.2.1 ipv6hint=2001:db8::1"}, nil, []string{"192.0.2.1", "2001:db8::1"}},
		{"related-additional", []string{"example.test. 60 IN HTTPS 1 svc.test. alpn=h2", "example.test. 60 IN NS ns.test."}, []string{
			"svc.test. 60 IN A 192.0.2.1", "svc.test. 60 IN AAAA 2001:db8::1",
			"ns.test. 60 IN A 192.0.2.99", "unrelated.test. 60 IN HTTPS 1 . ipv4hint=192.0.2.98",
		}, []string{"192.0.2.1", "2001:db8::1"}},
		{"CNAME-and-service-chain", []string{"EXAMPLE.test. 60 IN CNAME alias.test.", "alias.test. 60 IN HTTPS 0 binding.test."}, []string{
			"last.test. 60 IN A 192.0.2.1", "svc.test. 60 IN CNAME last.test.",
			"binding.test. 60 IN HTTPS 1 svc.test. ipv6hint=2001:db8::1",
		}, []string{"192.0.2.1", "2001:db8::1"}},
		{"AliasMode-precedence", []string{
			"example.test. 60 IN HTTPS 1 ignored.test. ipv4hint=192.0.2.99",
			"example.test. 60 IN HTTPS 0 alias.test. ipv4hint=192.0.2.98",
		}, []string{"ignored.test. 60 IN A 192.0.2.97", "alias.test. 60 IN HTTPS 1 . ipv4hint=192.0.2.1"}, []string{"192.0.2.1"}},
		{"SVCB-alias-chain", []string{"example.test. 60 IN SVCB 0 alias.test."}, []string{"alias.test. 60 IN SVCB 1 svc.test.", "svc.test. 60 IN A 192.0.2.1"}, []string{"192.0.2.1"}},
		{"dot-target-additional", []string{"example.test. 60 IN HTTPS 1 . alpn=h2"}, []string{"example.test. 60 IN A 192.0.2.1"}, []string{"192.0.2.1"}},
		{"CNAME-root-target", []string{"example.test. 60 IN CNAME ."}, []string{". 60 IN A 192.0.2.1"}, []string{"192.0.2.1"}},
		{"alias-loop", []string{"example.test. 60 IN HTTPS 0 alias.test."}, []string{"alias.test. 60 IN CNAME example.test."}, nil},
		{"unavailable", []string{"example.test. 60 IN HTTPS 0 ."}, []string{"unrelated.test. 60 IN A 192.0.2.99"}, nil},
		{"no-address", []string{"example.test. 60 IN HTTPS 1 . alpn=h2 ech=AA=="}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &D.Msg{Answer: hookTestRecords(t, tc.answer), Extra: hookTestRecords(t, tc.extra)}
			// Authority records and unassociated addresses are not business targets.
			m.Ns = hookTestRecords(t, []string{"example.test. 60 IN A 192.0.2.96"})
			before := m.String()
			var got []string
			for _, ip := range hookResponseIPs("Example.Test", m) {
				got = append(got, ip.Unmap().String())
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("addresses = %v, want %v", got, tc.want)
			}
			if m.String() != before {
				t.Fatal("hook changed the DNS response")
			}
		})
	}
}

type recordHookDNSClient struct {
	answer, extra []D.RR
	calls         atomic.Int32
}

func (*recordHookDNSClient) Address() string  { return "record-hook-test" }
func (*recordHookDNSClient) ResetConnection() {}
func (c *recordHookDNSClient) ExchangeContext(_ context.Context, req *D.Msg) (*D.Msg, error) {
	c.calls.Add(1)
	m := new(D.Msg)
	m.SetReply(req)
	for _, rr := range c.answer {
		m.Answer = append(m.Answer, D.Copy(rr))
	}
	for _, rr := range c.extra {
		m.Extra = append(m.Extra, D.Copy(rr))
	}
	return m, nil
}

func TestDNSServiceBindingHooks(t *testing.T) {
	pool, err := fakeip.New(fakeip.Options{IPNet: netip.MustParsePrefix("198.18.0.0/16"), Size: 8})
	if err != nil {
		t.Fatal(err)
	}
	previous := resolver.DefaultHostMapper
	resolver.DefaultHostMapper = &ResolverEnhancer{mode: C.DNSFakeIP, fakeIPPool: pool}
	defer func() { resolver.DefaultHostMapper = previous }()

	for _, tc := range []struct {
		name                string
		qtype               uint16
		answer, extra, want []string
		fake, skip          bool
	}{
		{"HTTPS", D.TypeHTTPS, []string{"example.test. 300 IN HTTPS 1 . ipv4hint=192.0.2.1,198.18.0.4 ipv6hint=2001:db8::1"}, []string{"example.test. 300 IN A 192.0.2.1"}, []string{"192.0.2.1", "2001:db8::1"}, false, false},
		{"SVCB", D.TypeSVCB, []string{"example.test. 300 IN SVCB 1 . ipv4hint=192.0.2.1 ipv6hint=2001:db8::1"}, nil, []string{"192.0.2.1", "2001:db8::1"}, false, false},
		{"Additional", D.TypeHTTPS, []string{"example.test. 300 IN HTTPS 1 svc.test. alpn=h2"}, []string{"svc.test. 300 IN A 192.0.2.1", "ns.test. 300 IN A 192.0.2.99"}, []string{"192.0.2.1"}, false, false},
		{"metadata-only", D.TypeHTTPS, []string{"example.test. 300 IN HTTPS 1 . alpn=h2"}, nil, nil, false, false},
		{"fake-ip-empty", D.TypeHTTPS, []string{"example.test. 300 IN HTTPS 1 . ipv4hint=192.0.2.1"}, nil, nil, true, false},
		{"fake-ip-exception", D.TypeHTTPS, []string{"example.test. 300 IN HTTPS 1 . ipv4hint=192.0.2.1"}, nil, []string{"192.0.2.1"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan hook.Event, 8)
			var reject atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var event hook.Event
				if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				events <- event
				if event.Event == "IP" && reject.Load() {
					w.WriteHeader(503)
				}
			}))
			defer server.Close()
			cfg, err := hook.Parse(map[string]hook.ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL, OnError: "block"}}, []hook.RuleConfig{
				{Event: "HOST", Match: "DOMAIN,example.test", Providers: []string{"p"}},
				{Event: "IP", Match: "DOMAIN,example.test", Providers: []string{"p"}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			hook.Apply(cfg)
			defer hook.Shutdown()
			client := &recordHookDNSClient{answer: hookTestRecords(t, tc.answer), extra: hookTestRecords(t, tc.extra)}
			mapper := &ResolverEnhancer{ipv6: true}
			if tc.fake {
				mapper.mode, mapper.fakeIPSkipper = C.DNSFakeIP, &fakeip.Skipper{}
				if tc.skip {
					mapper.fakeIPSkipper.Mode = C.FilterWhiteList
				}
			}
			service := NewService(NewResolverFromClient(client), mapper)
			req := new(D.Msg)
			req.SetQuestion("Example.Test.", tc.qtype)
			for attempt := 0; attempt < 2; attempt++ {
				resp, err := service.ServeMsg(context.Background(), req)
				if err != nil || resp == nil {
					t.Fatal("DNS failed", err)
				}
				wantEvents := 1
				if len(tc.want) > 0 {
					wantEvents++
				}
				if len(events) != wantEvents {
					t.Fatalf("hook calls = %d, want %d", len(events), wantEvents)
				}
				for i := 0; i < wantEvents; i++ {
					event := <-events
					if event.Host != "example.test" || event.Source != "dns" || event.Network != "" {
						t.Fatalf("unexpected DNS event: %+v", event)
					}
					if event.Event == "IP" {
						slices.Sort(event.IPs)
						if !slices.Equal(event.IPs, tc.want) {
							t.Fatalf("event addresses = %v, want %v", event.IPs, tc.want)
						}
					}
				}
			}
			queries := int32(1)
			if tc.fake && !tc.skip {
				queries = 0
			}
			if client.calls.Load() != queries {
				t.Fatal("cache or fake-ip path made an unexpected upstream query", client.calls.Load())
			}
			if len(tc.want) > 0 {
				reject.Store(true)
				resp, err := service.ServeMsg(context.Background(), req)
				if resp != nil || !bridge.IsBlocked(err) || client.calls.Load() != queries {
					t.Fatal("cached service-binding response escaped a blocking IP hook", resp, err)
				}
			}
		})
	}
}
