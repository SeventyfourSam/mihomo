package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"sync/atomic"
	"time"

	bridge "github.com/metacubex/mihomo/common/hook"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/log"
)

type Event struct {
	Version     int       `json:"version"`
	EventID     string    `json:"event_id"`
	Event       string    `json:"event"`
	ObservedAt  time.Time `json:"observed_at"`
	Host        string    `json:"host"`
	IPs         []string  `json:"ips,omitempty"`
	Source      string    `json:"source"`
	Network     string    `json:"network,omitempty"`
	Process     string    `json:"process,omitempty"`
	ProcessPath string    `json:"process_path,omitempty"`
	Outbound    string    `json:"outbound,omitempty"`
}

type job struct {
	name string
	p    ProviderConfig
	e    Event
}

type Engine struct {
	cfg       *Config
	ctx       context.Context
	cancel    context.CancelFunc
	queue     chan job
	transport *http.Transport
	client    *http.Client
}

var current atomic.Pointer[Engine]

func Enabled() bool { return current.Load() != nil }

// Apply atomically publishes a validated config; an empty config disables hooks.
func Apply(cfg *Config) {
	var next *Engine
	if cfg != nil && len(cfg.rules) != 0 {
		next = newEngine(cfg)
	}
	if previous := current.Swap(next); previous != nil {
		previous.cancel()
		previous.transport.CloseIdleConnections()
	}
}

func Shutdown() { Apply(nil) }

func newEngine(cfg *Config) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	t := &http.Transport{
		// Hooks use the socket policy, but never the business proxy or env proxy.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(bridge.Without(ctx), network, address)
		},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 2 * time.Second,
	}
	e := &Engine{cfg: cfg, ctx: ctx, cancel: cancel, queue: make(chan job, 256), transport: t}
	e.client = &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i := 0; i < 4; i++ {
		go e.worker()
	}
	return e
}

func (e *Engine) execute(ctx context.Context, p ProviderConfig, event Event) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	if e.ctx.Err() != nil {
		return e.ctx.Err()
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if p.Type == "script" {
		cmd := exec.CommandContext(ctx, p.Command, p.Args...)
		cmd.Stdin = bytes.NewReader(body)
		cmd.WaitDelay = 100 * time.Millisecond
		return cmd.Run()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range p.Headers {
		req.Header.Set(name, value)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != p.SuccessStatus {
		return fmt.Errorf("HTTP status %d, expected %d", resp.StatusCode, p.SuccessStatus)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return err
}

func (e *Engine) dispatch(ctx context.Context, name string, p ProviderConfig, event Event) (bool, error) {
	if p.Mode == "async" {
		select {
		case <-e.ctx.Done():
			log.Warnln("[Hook] provider %s: engine stopped", name)
		case e.queue <- job{name, p, event}:
			return true, nil
		default:
			log.Warnln("[Hook] provider %s: async queue full", name)
		}
		return false, nil
	}
	if err := e.execute(ctx, p, event); err != nil {
		if p.OnError == "block" {
			return false, fmt.Errorf("%w: provider %s: %v", bridge.ErrBlocked, name, err)
		}
		log.Warnln("[Hook] provider %s failed: %v", name, err)
		return false, nil
	}
	return true, nil
}

func (e *Engine) worker() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case j := <-e.queue:
			for attempt := 0; attempt < 3; attempt++ {
				err := e.execute(e.ctx, j.p, j.e)
				if err == nil {
					break
				}
				if attempt == 2 {
					log.Warnln("[Hook] provider %s failed after retries: %v", j.name, err)
					break
				}
				timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
				select {
				case <-e.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}
	}
}
