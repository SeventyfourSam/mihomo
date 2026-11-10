package hook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bridge "github.com/metacubex/mihomo/common/hook"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	R "github.com/metacubex/mihomo/rules"
	RP "github.com/metacubex/mihomo/rules/provider"
)

func install(t *testing.T, url, mode, event, match string) *Config {
	t.Helper()
	cfg, err := Parse(map[string]ProviderConfig{"test": {Type: "api", Mode: mode, URL: url}}, []RuleConfig{{event, match, []string{"test"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	Apply(cfg)
	t.Cleanup(Shutdown)
	return cfg
}

func metadata() *C.Metadata {
	return &C.Metadata{Host: "Example.Test", NetWork: C.TCP, DstPort: 443, Process: "app.exe"}
}

func TestSyncRejectionCannotBeBypassed(t *testing.T) {
	var status atomic.Int32
	status.Store(503)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	install(t, server.URL, "sync", "IP", "MATCH")
	s := NewSession(metadata(), false)
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.1")}
	if err := s.IPs(context.Background(), ips, "resolver", 443); !errors.Is(err, bridge.ErrBlocked) {
		t.Fatal("expected blocking error", err)
	}
	status.Store(200)
	if err := s.BeforeDial(context.Background(), ips[0], "443"); !bridge.IsBlocked(err) || calls.Load() != 1 {
		t.Fatal("a dial retry bypassed the earlier rejection", err, calls.Load())
	}
	s.RetryPacket()
	if err := s.IPs(context.Background(), ips, "connection", 443); err != nil || calls.Load() != 2 {
		t.Fatal("a new packet operation did not retry", err, calls.Load())
	}
	if err := s.IPs(context.Background(), ips, "connection", 443); err != nil || calls.Load() != 2 {
		t.Fatal("confirmed address was not reused", err, calls.Load())
	}
}

func TestRouteCollectsBeforeCallingProviders(t *testing.T) {
	events := make(chan Event, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			t.Error(err)
		}
		events <- e
		w.WriteHeader(200)
	}))
	defer server.Close()
	install(t, server.URL, "sync", "IP", "DOMAIN-SUFFIX,test")
	m := metadata()
	s := NewSession(m, false)
	s.Collect()
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")}
	if err := s.Lookup(context.Background(), m.Host, ips); err != nil || len(events) != 0 {
		t.Fatal("provider ran inside routing", err)
	}
	if err := s.FinishRoute(context.Background(), m, "DIRECT"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatal("missing event")
	}
	event := <-events
	if len(event.IPs) != 2 || event.Host != "example.test" || event.Process != "app.exe" {
		t.Fatal("wrong event", event)
	}
	if err := s.BeforeDial(context.Background(), ips[1], "443"); err != nil || len(events) != 0 {
		t.Fatal("final dial did not reuse route confirmation", err)
	}
}

func TestAsyncReturnsAndRetriesSameEvent(t *testing.T) {
	entered := make(chan Event, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		_ = json.NewDecoder(r.Body).Decode(&e)
		entered <- e
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
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
	install(t, server.URL, "async", "HOST", "MATCH")
	done := make(chan error, 1)
	go func() { done <- NewSession(metadata(), false).Host(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("async notification blocked caller")
	}
	first := <-entered
	close(release)
	select {
	case next := <-entered:
		if next.EventID != first.EventID || next.ObservedAt != first.ObservedAt {
			t.Fatal("retry changed event identity")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("async request was not retried")
	}
}

type testRuleSet struct {
	strategy any
	behavior P.RuleBehavior
}

func (p testRuleSet) Name() string               { return "set" }
func (p testRuleSet) VehicleType() P.VehicleType { return P.Inline }
func (p testRuleSet) Type() P.ProviderType       { return P.Rule }
func (p testRuleSet) Initial() error             { return nil }
func (p testRuleSet) Update() error              { return nil }
func (p testRuleSet) Behavior() P.RuleBehavior   { return p.behavior }
func (p testRuleSet) Count() int                 { return 1 }
func (p testRuleSet) Strategy() any              { return p.strategy }
func (p testRuleSet) Match(m *C.Metadata, helper C.RuleMatchHelper) bool {
	return p.strategy.(interface {
		Match(*C.Metadata, C.RuleMatchHelper) bool
	}).Match(m, helper)
}

func TestUnknownFieldsIncludingClassicalAndNot(t *testing.T) {
	strategy := RP.NewClassicalStrategy(R.ParseRule)
	strategy.Insert("NOT,((PROCESS-NAME,app.exe))")
	cases := []struct {
		match    string
		expected truth
	}{
		{"NOT,((PROCESS-NAME,app.exe))", unknown},
		{"OR,((DOMAIN,example.test),(PROCESS-NAME,app.exe))", yes},
		{"AND,((DOMAIN,example.test),(PROCESS-NAME,app.exe))", unknown},
		{"NOT,((IP-CIDR,192.0.2.0/24))", unknown},
		{"RULE-SET,set", unknown},
		{"NOT,((NETWORK,tcp))", unknown},
	}
	for _, tc := range cases {
		t.Run(tc.match, func(t *testing.T) {
			cfg, err := Parse(map[string]ProviderConfig{"p": {Type: "api", URL: "http://127.0.0.1"}}, []RuleConfig{{"HOST", tc.match, []string{"p"}}}, map[string]P.RuleProvider{"set": testRuleSet{strategy, P.Classical}})
			if err != nil {
				t.Fatal(err)
			}
			m := &C.Metadata{Host: "example.test", NetWork: C.InvalidNet}
			if got := cfg.matches(cfg.rules[0].match, m, true); got != tc.expected {
				t.Fatalf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestMatchingIPsOnly(t *testing.T) {
	got := make(chan Event, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		_ = json.NewDecoder(r.Body).Decode(&e)
		got <- e
	}))
	defer server.Close()
	install(t, server.URL, "sync", "IP", "IP-CIDR,192.0.2.0/24")
	s := NewSession(metadata(), false)
	if err := s.IPs(context.Background(), []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("198.51.100.1")}, "resolver", 443); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatal("missing event")
	}
	event := <-got
	if len(event.IPs) != 1 || event.IPs[0] != "192.0.2.1" {
		t.Fatal(event)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, p := range []ProviderConfig{
		{Type: "api", URL: "file:///tmp/x"},
		{Type: "api", URL: "http://127.0.0.1", Mode: "invalid"},
		{Type: "api", URL: "http://127.0.0.1", Mode: "async", OnError: "block"},
		{Type: "api", URL: "http://127.0.0.1", Timeout: -time.Second},
		{Type: "script", Command: "codex-mihomo-no-such-executable"},
	} {
		if _, err := Parse(map[string]ProviderConfig{"p": p}, nil, nil); err == nil {
			t.Fatal("accepted invalid provider", p)
		}
	}
	if _, err := Parse(nil, []RuleConfig{{"IP", "MATCH", []string{"missing"}}}, nil); err == nil {
		t.Fatal("accepted unknown provider")
	}
}

func TestScriptUsesJSONStdinAndExitStatus(t *testing.T) {
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e := newEngine(&Config{})
	defer e.cancel()
	p := ProviderConfig{Type: "script", Command: command, Args: []string{"-test.run=^TestHookScriptHelper$"}, Timeout: time.Second}
	t.Setenv("MIHOMO_HOOK_TEST_HELPER", "1")
	event := Event{Version: 1, Event: "HOST", Host: "example.test"}
	if err := e.execute(context.Background(), p, event); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIHOMO_HOOK_TEST_HELPER", "fail")
	if err := e.execute(context.Background(), p, event); err == nil {
		t.Fatal("ignored script exit status")
	}
}

func TestHookScriptHelper(t *testing.T) {
	mode := os.Getenv("MIHOMO_HOOK_TEST_HELPER")
	if mode == "" {
		return
	}
	var e Event
	if json.NewDecoder(os.Stdin).Decode(&e) != nil || e.Host != "example.test" || e.Event != "HOST" || strings.Contains(e.Host, "\n") {
		os.Exit(3)
	}
	if mode == "fail" {
		os.Exit(4)
	}
	os.Exit(0)
}

func TestConfirmationExpiryAndProviderOrder(t *testing.T) {
	seen := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen <- r.Header.Get("X-Provider") }))
	defer server.Close()
	providers := map[string]ProviderConfig{
		"first":  {Type: "api", Mode: "sync", URL: server.URL, Headers: map[string]string{"X-Provider": "first"}},
		"second": {Type: "api", Mode: "sync", URL: server.URL, Headers: map[string]string{"X-Provider": "second"}},
	}
	cfg, err := Parse(providers, []RuleConfig{{"IP", "MATCH", []string{"second", "first"}}, {"IP", "MATCH", []string{"second"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	Apply(cfg)
	defer Shutdown()
	s := NewSession(metadata(), false)
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.1")}
	if err := s.IPs(context.Background(), ips, "connection", 443); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || <-seen != "second" || <-seen != "first" {
		t.Fatal("provider order or deduplication changed")
	}
	for key := range s.confirmed {
		s.confirmed[key] = time.Now().Add(-time.Second)
	}
	if err := s.IPs(context.Background(), ips, "connection", 443); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatal("expired approval was reused")
	}
}

func TestSyncTimeoutBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer server.Close()
	cfg, err := Parse(map[string]ProviderConfig{"p": {Type: "api", Mode: "sync", URL: server.URL, Timeout: 30 * time.Millisecond}}, []RuleConfig{{"IP", "MATCH", []string{"p"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	Apply(cfg)
	defer Shutdown()
	s := NewSession(metadata(), false)
	if err := s.IPs(context.Background(), []netip.Addr{netip.MustParseAddr("192.0.2.1")}, "connection", 443); !bridge.IsBlocked(err) {
		t.Fatal("timeout did not block", err)
	}
}
