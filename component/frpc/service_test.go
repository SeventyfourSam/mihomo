package frpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/log"
)

func localConfig(t *testing.T, port int) *Config {
	t.Helper()
	cfg, err := parseYAML(strings.ReplaceAll(strings.ReplaceAll(validYAML, "frps.example.test", "127.0.0.1"), "serverPort: 8443", fmt.Sprintf("serverPort: %d", port)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCancelDuringTLSAndReload(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c // Intentionally never finish the TLS handshake.
		}
	}()
	var manager Manager
	t.Cleanup(manager.Stop)
	port := l.Addr().(*net.TCPAddr).Port
	start := time.Now()
	manager.Apply(localConfig(t, port))
	if time.Since(start) > time.Second {
		t.Fatal("Apply waited for the server")
	}
	var c net.Conn
	select {
	case c = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not dial")
	}
	defer c.Close()
	first := manager.current
	manager.Apply(localConfig(t, port))
	if manager.current != first {
		t.Fatal("unchanged config restarted client")
	}
	start = time.Now()
	manager.Stop()
	if time.Since(start) > time.Second {
		t.Fatal("cancellation waited for TLS timeout")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("old service not stopped")
	}
	manager.Apply(localConfig(t, port))
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("client did not restart after disable")
	}
}

func TestImportDoesNotChangeQUICEnvironment(t *testing.T) {
	const probe = "MIHOMO_FRPC_GLOBAL_PROBE"
	if os.Getenv(probe) == "1" {
		if os.Getenv("QUIC_GO_DISABLE_ECN") != "false" || os.Getenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING") != "sentinel" {
			t.Fatal("frp import mutated QUIC environment")
		}
		_, pattern := http.DefaultServeMux.Handler(httptest.NewRequest("GET", "/debug/pprof/", nil))
		if pattern != "" {
			t.Fatal("frp import registered a default pprof handler")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestImportDoesNotChangeQUICEnvironment$")
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "QUIC_GO_DISABLE_") && !strings.HasPrefix(e, probe+"=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, probe+"=1", "QUIC_GO_DISABLE_ECN=false", "QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING=sentinel")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}

func TestConnectorUsesMihomoSocketPolicy(t *testing.T) {
	// A failing mihomo socket hook must also fail the embedded connector.
	// This proves it does not silently use net.Dial behind the TUN policy.
	previous := dialer.DefaultInterface.Load()
	dialer.DefaultInterface.Store("mihomo-frpc-no-such-interface")
	defer dialer.DefaultInterface.Store(previous)
	cfg, err := parseYAML(strings.ReplaceAll(validYAML, "frps.example.test", "192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c := newConnector(ctx, &cfg.common)
	defer c.Close()
	err = c.Open()
	if err == nil || !strings.Contains(err.Error(), "interface") {
		t.Fatalf("expected interface binding failure, got %v", err)
	}
}

func TestLogBridgeRedactsToken(t *testing.T) {
	sub := log.Subscribe()
	defer log.UnSubscribe(sub)
	var writer logWriter
	writer.settings.Store(&logSettings{level: 1, token: "sensitive-token"})
	_, _ = writer.Write([]byte("login rejected sensitive-token"))
	select {
	case event := <-sub:
		if strings.Contains(event.Payload, "sensitive-token") || !strings.Contains(event.Payload, "[FRPC]") {
			t.Fatal("incorrect log bridge")
		}
	case <-time.After(time.Second):
		t.Fatal("FRP log missing from mihomo event stream")
	}
}

func TestUnavailableServerDoesNotBlock(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, p, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(p)
	l.Close()
	var m Manager
	m.Apply(localConfig(t, port))
	defer m.Stop()
	select {
	case <-m.current.done:
		t.Fatal("client exited when frps was unavailable")
	case <-time.After(100 * time.Millisecond):
	}
}
