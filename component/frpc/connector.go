package frpc

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/transport"
	fmux "github.com/hashicorp/yamux"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/resolver"
)

// connector implements the supported TCP + optional TLS + yamux transport.
// Physical connections always use mihomo's socket protection/interface binding
// and bootstrap DNS, with no proxy rules, http_proxy or net.DefaultResolver.
type connector struct {
	ctx        context.Context
	cfg        *v1.ClientCommonConfig
	session    *fmux.Session
	conn       net.Conn
	stopCancel func() bool
	closeOnce  sync.Once
}

func newConnector(ctx context.Context, cfg *v1.ClientCommonConfig) client.Connector {
	return &connector{ctx: ctx, cfg: cfg}
}

func (c *connector) Open() (err error) {
	ctx, cancel := context.WithTimeout(c.ctx, time.Duration(c.cfg.Transport.DialServerTimeout)*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(c.cfg.ServerAddr, strconv.Itoa(c.cfg.ServerPort)),
		dialer.WithResolver(resolver.SystemResolver))
	if err != nil {
		return err
	}
	c.conn = conn
	// Cancel the physical socket even during TLS/login reads. A cancelled
	// service must not retain a session until its network timeout expires.
	c.stopCancel = context.AfterFunc(c.ctx, func() { _ = conn.Close() })
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	if *c.cfg.Transport.TLS.Enable {
		tlsConfig, e := transport.NewClientTLSConfig("", "", "", c.cfg.ServerAddr)
		if e != nil {
			return e
		}
		// v0.62.1 defaults to normal TLS framing (no custom first byte).
		tlsConn := tls.Client(conn, tlsConfig)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			return err
		}
		c.conn = tlsConn
	}
	muxConfig := fmux.DefaultConfig()
	muxConfig.KeepAliveInterval = time.Duration(c.cfg.Transport.TCPMuxKeepaliveInterval) * time.Second
	muxConfig.LogOutput = io.Discard
	muxConfig.MaxStreamWindowSize = 6 * 1024 * 1024
	c.session, err = fmux.Client(c.conn, muxConfig)
	return err
}

func (c *connector) Connect() (net.Conn, error) {
	return c.session.OpenStream()
}

func (c *connector) Close() error {
	c.closeOnce.Do(func() {
		if c.stopCancel != nil {
			c.stopCancel()
		}
		if c.session != nil {
			_ = c.session.Close()
		}
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
	return nil
}
