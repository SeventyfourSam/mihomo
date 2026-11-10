package hook

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	bridge "github.com/metacubex/mihomo/common/hook"
	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/constant/features"
)

type confirmation struct {
	provider, host, ip string
	port               uint16
}

// Session owns state for one business operation, never a global IP cache.
type Session struct {
	engine    *Engine
	meta      C.Metadata
	dns       bool
	collect   bool
	pending   []netip.Addr
	hosts     map[string]bool
	outbound  string
	mu        sync.Mutex // metadata and observations; never held during external I/O
	execMu    sync.Mutex // serialize providers only within this business operation
	confirmed map[confirmation]time.Time
	failed    error
}

func NewSession(metadata *C.Metadata, allowProcess bool) *Session {
	e := current.Load()
	if e == nil || metadata.Type == C.INNER {
		return nil
	}
	m := *metadata
	if e.cfg.needProcess && allowProcess && m.SourceValid() && m.Process == "" && m.ProcessPath == "" {
		findProcess(&m)
		metadata.Process, metadata.ProcessPath, metadata.Uid = m.Process, m.ProcessPath, m.Uid
	}
	return newSession(e, m, false)
}

func NewDNS(host string) *Session {
	if e := current.Load(); e != nil {
		return newSession(e, C.Metadata{Host: normalizeHost(host), NetWork: C.InvalidNet}, true)
	}
	return nil
}

func newSession(e *Engine, m C.Metadata, dns bool) *Session {
	s := &Session{engine: e, meta: m, dns: dns, hosts: make(map[string]bool), confirmed: make(map[confirmation]time.Time)}
	s.addHosts(m)
	return s
}

func findProcess(m *C.Metadata) {
	if !features.CMFA {
		uid, path, err := process.FindProcessName(m.NetWork.String(), m.SrcIP, int(m.SrcPort))
		if err == nil {
			m.Uid, m.ProcessPath, m.Process = uid, path, filepath.Base(path)
		}
	}
	if pkg, err := process.FindPackageName(m); err == nil {
		m.Process = pkg
	}
}

func normalizeHost(host string) string { return strings.ToLower(strings.TrimRight(host, ".")) }

func (s *Session) addHosts(m C.Metadata) {
	for _, host := range []string{m.Host, m.RuleHost()} {
		if host != "" {
			s.hosts[normalizeHost(host)] = true
		}
	}
}

func (s *Session) Context(ctx context.Context) context.Context {
	if s == nil {
		return ctx
	}
	return bridge.WithObserver(ctx, s, "")
}

func FromContext(ctx context.Context) *Session {
	s, _ := bridge.ObserverFrom(ctx).(*Session)
	return s
}

func (s *Session) Current() bool { return s != nil && s.engine == current.Load() }

func (s *Session) Collect() {
	if s != nil {
		s.collect = true
	}
}

// FinishRoute runs after the routing lock is released. DNS in rule matching only
// collects results, so callbacks cannot hold the routing lock across API calls.
func (s *Session) FinishRoute(ctx context.Context, metadata *C.Metadata, outbound string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.meta, s.outbound, s.collect = *metadata, outbound, false
	s.addHosts(*metadata)
	ips := append(s.pending, metadata.DstIP)
	s.pending = nil
	s.mu.Unlock()
	if err := s.Host(ctx); err != nil {
		return err
	}
	return s.IPs(ctx, ips, "connection", metadata.DstPort)
}

func (s *Session) Lookup(ctx context.Context, host string, ips []netip.Addr) error {
	s.mu.Lock()
	accepted := s.hosts[normalizeHost(host)]
	if ip, err := netip.ParseAddr(host); err == nil {
		accepted = ip.Unmap() == s.meta.DstIP.Unmap()
	}
	if !accepted {
		s.mu.Unlock()
		return nil
	}
	if s.collect {
		s.pending = append(s.pending, ips...)
		s.mu.Unlock()
		return nil
	}
	port := s.meta.DstPort
	s.mu.Unlock()
	return s.IPs(ctx, ips, "resolver", port)
}

func (s *Session) BeforeDial(ctx context.Context, ip netip.Addr, port string) error {
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return err
	}
	return s.IPs(ctx, []netip.Addr{ip}, "connection", uint16(p))
}

func (s *Session) Host(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.emit(ctx, "HOST", nil, "connection", 0)
}

func (s *Session) IPs(ctx context.Context, ips []netip.Addr, source string, port uint16) error {
	if s == nil {
		return nil
	}
	var real []netip.Addr
	seen := make(map[netip.Addr]bool)
	for _, ip := range ips {
		ip = ip.Unmap()
		if ip.IsValid() && !resolver.IsFakeIP(ip) && !seen[ip] {
			real = append(real, ip)
			seen[ip] = true
		}
	}
	if len(real) == 0 {
		return nil
	}
	return s.emit(ctx, "IP", real, source, port)
}

func (s *Session) emit(ctx context.Context, event string, ips []netip.Addr, source string, port uint16) error {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	s.mu.Lock()
	m, outbound := s.meta, s.outbound
	s.mu.Unlock()
	m.Host, m.SniffHost = normalizeHost(m.Host), normalizeHost(m.SniffHost)
	host := normalizeHost(m.RuleHost())
	if event == "HOST" && host == "" {
		return nil
	}
	if s.engine.ctx.Err() != nil {
		s.failed = fmt.Errorf("%w: configuration changed", bridge.ErrBlocked)
		return s.failed
	}
	if event == "IP" {
		m.DstPort = port
	}
	base := Event{Version: 1, EventID: utils.NewUUIDV4().String(), Event: event, ObservedAt: time.Now().UTC(), Host: host,
		Source: source, Process: m.Process, ProcessPath: m.ProcessPath, Outbound: outbound}
	if s.dns {
		base.Source = "dns"
	}
	if m.NetWork == C.TCP || m.NetWork == C.UDP {
		base.Network = m.NetWork.String()
	}
	order := []string{}
	selected := make(map[string]map[string]bool)
	values := ips
	if event == "HOST" {
		values = []netip.Addr{m.DstIP}
	}
	for _, r := range s.engine.cfg.rules {
		if r.event != event {
			continue
		}
		for _, ip := range values {
			copyM := m
			copyM.DstIP = ip
			if s.engine.cfg.matches(r.match, &copyM, s.dns) != yes {
				continue
			}
			ipText := ""
			if event == "IP" {
				ipText = ip.String()
			}
			for _, name := range r.providers {
				key := confirmation{name, host, ipText, port}
				if until, ok := s.confirmed[key]; ok && (event == "HOST" || time.Now().Before(until)) {
					continue
				}
				if selected[name] == nil {
					order = append(order, name)
					selected[name] = make(map[string]bool)
				}
				selected[name][ipText] = true
			}
		}
	}
	for _, name := range order {
		p := s.engine.cfg.providers[name]
		e := base
		for _, ip := range ips {
			if selected[name][ip.String()] {
				e.IPs = append(e.IPs, ip.String())
			}
		}
		ok, err := s.engine.dispatch(ctx, name, p, e)
		if err != nil {
			s.failed = err // ordinary DNS/dial retries cannot bypass this rejection
			return err
		}
		if ok {
			if len(s.confirmed) > 512 {
				clear(s.confirmed) // eviction only causes another confirmation
			}
			for ip := range selected[name] {
				s.confirmed[confirmation{name, host, ip, port}] = time.Now().Add(p.ConfirmationTTL)
			}
		}
	}
	return nil
}

// RetryPacket starts another UDP send operation without treating a previous
// failure as success. Successful address confirmations remain lease-bounded.
func (s *Session) RetryPacket() {
	s.execMu.Lock()
	s.failed = nil
	s.execMu.Unlock()
}

// ForAdapter authorizes only the actual business-target dial. Proxy groups pass
// the context through; proxy transport endpoints lose it in ForDial/INNER calls.
func ForAdapter(ctx context.Context, metadata *C.Metadata, adapter C.ProxyAdapter) (context.Context, error) {
	s := FromContext(ctx)
	if s == nil {
		return ctx, nil
	}
	if metadata.Type == C.INNER {
		return bridge.Without(ctx), nil
	}
	switch adapter.Type() {
	case C.Relay, C.Selector, C.Fallback, C.URLTest, C.LoadBalance:
		return ctx, nil
	}
	if metadata.DstIP.IsValid() {
		if err := s.IPs(ctx, []netip.Addr{metadata.DstIP}, "connection", metadata.DstPort); err != nil {
			return ctx, err
		}
	}
	address := ""
	if adapter.Type() == C.Direct || adapter.IsL3Protocol(metadata) {
		address = metadata.RemoteAddress()
	}
	return bridge.WithObserver(ctx, s, address), nil
}
