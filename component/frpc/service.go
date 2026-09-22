package frpc

import (
	"context"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	"github.com/metacubex/mihomo/log"
)

// Default is owned by mihomo's executor, alongside its listeners and DNS.
var Default Manager

// Manager serializes replacement services without waiting for network access
// in Apply. A replacement starts only after the previous service has stopped.
type Manager struct {
	mu      sync.Mutex
	current *instance
}

type instance struct {
	config *Config
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *Manager) Apply(cfg *Config) {
	m.apply(cfg)
}

func (m *Manager) apply(cfg *Config) *instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil && cfg == nil {
		return nil
	}
	if m.current != nil && equalConfig(m.current.config, cfg) {
		return m.current
	}
	var previous <-chan struct{}
	if m.current != nil {
		m.current.cancel()
		previous = m.current.done
	}
	ctx, cancel := context.WithCancel(context.Background())
	i := &instance{config: cfg, cancel: cancel, done: make(chan struct{})}
	m.current = i
	go func() {
		defer close(i.done)
		if previous != nil {
			// Even a superseded replacement must keep this chain intact so
			// later reloads cannot overlap an older, still-closing session.
			<-previous
		}
		if ctx.Err() != nil || cfg == nil {
			return
		}
		output.settings.Store(&logSettings{level: cfg.logLevel, token: cfg.common.Auth.Token})
		log.Infoln("[FRPC] starting embedded client; connection runs in the background")
		run(ctx, cfg)
		log.Infoln("[FRPC] client stopped")
	}()
	return i
}

func equalConfig(a, b *Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.identity == b.identity
}

// Stop waits for cancellation and connection cleanup, including during the
// first connection attempt. It is safe before startup and after disabling.
func (m *Manager) Stop() {
	i := m.apply(nil)
	if i == nil {
		return
	}
	select {
	case <-i.done:
	case <-time.After(15 * time.Second):
		log.Errorln("[FRPC] timed out waiting for client shutdown")
	}
}

func run(ctx context.Context, cfg *Config) {
	for ctx.Err() == nil {
		common := cfg.common
		service, err := client.NewService(client.ServiceOptions{
			Common: &common, ProxyCfgs: cfg.proxies, ConnectorCreator: newConnector,
		})
		if err == nil {
			err = service.Run(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		// Normal network failures are retried by frp itself. This is only a
		// supervisor for an unexpected return from the client service.
		log.Errorln("[FRPC] client exited unexpectedly: %v; retrying in 5s", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
