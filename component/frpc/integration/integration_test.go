package integration

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/frpc"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/log"
)

// These tests use only ephemeral loopback ports and dummy credentials. They run
// the real frp client and mihomo SS listener/routing stack in-process. Only the
// test frps server is a separate process, matching the deployment boundary.
func TestMain(m *testing.M) {
	// Match main.go's process-lifetime resolver guard. Do not restore this
	// pointer while mihomo's background UDP workers may still be using it.
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("unexpected default resolver call")
	}}
	os.Exit(m.Run())
}

func reservePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 10; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenPacket("udp", address(p))
		l.Close()
		if err == nil {
			u.Close()
			return p
		}
	}
	t.Fatal("could not reserve TCP/UDP port")
	return 0
}

func address(port int) string { return net.JoinHostPort("127.0.0.1", fmt.Sprint(port)) }

func eventually(t *testing.T, label string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = check(); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: %v", label, err)
}

func echoBackend(t *testing.T) int {
	t.Helper()
	port := reservePort(t)
	l, err := net.Listen("tcp", address(port))
	if err != nil {
		t.Fatal(err)
	}
	u, err := net.ListenPacket("udp", address(port))
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close(); u.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, a, err := u.ReadFrom(buf)
			if err != nil {
				return
			}
			u.WriteTo(buf[:n], a)
		}
	}()
	return port
}

type testServer struct {
	cmd  *exec.Cmd
	once sync.Once
}

func (s *testServer) Close() {
	s.once.Do(func() {
		_ = s.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	})
}

func buildServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frps-fixture")
	cmd := exec.Command("go", "build", "-mod=readonly", "-race=false", "-o", path, "./internal/frpsfixture")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build frps fixture: %v\n%s", err, b)
	}
	return path
}

func startServer(t *testing.T, binary string, port int) *testServer {
	t.Helper()
	cmd := exec.Command(binary, fmt.Sprint(port))
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &testServer{cmd: cmd}
	t.Cleanup(s.Close)
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			if scanner.Text() == "FRPS_READY" {
				close(ready)
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("frps fixture did not start")
	}
	return s
}

func configuration(control, local, remote int, enabled bool, token string) string {
	return fmt.Sprintf(`mode: direct
log-level: warning
hosts:
  frps.integration.test: 127.0.0.1
listeners:
  - name: frp-ss
    type: shadowsocks
    listen: 127.0.0.1
    port: %d
    cipher: chacha20-ietf-poly1305
    password: integration-ss-password
    udp: true
    proxy: DIRECT
frpc:
  enable: %t
  serverAddr: frps.integration.test
  serverPort: %d
  auth: {method: token, token: %s}
  transport: {protocol: tcp, tls: {enable: true}}
  loginFailExit: false
  proxies:
    - {name: ss-tcp, type: tcp, localIP: 127.0.0.1, localPort: %d, remotePort: %d}
    - {name: ss-udp, type: udp, localIP: 127.0.0.1, localPort: %d, remotePort: %d}
`, local, enabled, control, token, local, remote, local, remote)
}

func apply(t *testing.T, text string) {
	t.Helper()
	c, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	executor.ApplyConfig(c, true)
}

func ssExchange(port, backend int, network string) error {
	ss, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "test", Server: "127.0.0.1", Port: port,
		Cipher: "chacha20-ietf-poly1305", Password: "integration-ss-password", UDP: true})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	meta := &C.Metadata{DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: uint16(backend), NetWork: C.TCP}
	payload := "native-frpc-ss-" + network
	buf := make([]byte, 2048)
	if network == "tcp" {
		c, err := ss.DialContext(ctx, meta)
		if err != nil {
			return err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		if _, err := io.WriteString(c, payload); err != nil {
			return err
		}
		n, err := io.ReadFull(c, buf[:len(payload)])
		if err != nil {
			return err
		}
		if string(buf[:n]) != payload {
			return fmt.Errorf("TCP echo mismatch")
		}
		return nil
	}
	meta.NetWork = C.UDP
	c, err := ss.ListenPacketContext(ctx, meta)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.WriteTo([]byte(payload), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: backend}); err != nil {
		return err
	}
	n, _, err := c.ReadFrom(buf)
	if err != nil {
		return err
	}
	if string(buf[:n]) != payload {
		return fmt.Errorf("UDP echo mismatch")
	}
	return nil
}

func TestShadowsocksTCPUDPReconnectAndReload(t *testing.T) {
	serverBinary := buildServer(t)
	C.SetHomeDir(t.TempDir())
	t.Cleanup(executor.Shutdown)
	// Exercise the same resolver guard as main.go. The server hostname must
	// resolve through mihomo's hosts/bootstrap path, never net.DefaultResolver.
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	control, local, remote := reservePort(t), reservePort(t), reservePort(t)
	backend := echoBackend(t)
	text := configuration(control, local, remote, true, "integration-token")
	start := time.Now()
	apply(t, text)
	if time.Since(start) > 2*time.Second {
		t.Fatal("startup blocked on offline frps")
	}
	// Local SS remains usable even while frps is unavailable.
	for _, protocol := range []string{"tcp", "udp"} {
		eventually(t, "offline local SS "+protocol, func() error { return ssExchange(local, backend, protocol) })
	}
	s := startServer(t, serverBinary, control)
	defer func() { s.Close() }()
	check := func(label string, port int) {
		for _, protocol := range []string{"tcp", "udp"} {
			eventually(t, label+" "+protocol, func() error { return ssExchange(port, backend, protocol) })
		}
	}
	check("initial FRP + SS", remote)
	t.Log("SS TCP/UDP forwarding through native frpc passed")
	s.Close()
	s = startServer(t, serverBinary, control)
	check("frps restart", remote)
	t.Log("SS TCP/UDP recovered after frps restart")
	if _, err := config.Parse([]byte(strings.ReplaceAll(text, "type: udp", "type: unsupported"))); err == nil {
		t.Fatal("bad reload was accepted")
	}
	check("invalid reload preserved service", remote)
	newRemote := reservePort(t)
	apply(t, configuration(control, local, newRemote, true, "integration-token"))
	check("changed remote port", newRemote)
	t.Log("configuration reload moved both forwarding ports")
	// Server-side auth failures must be visible without blocking application.
	sub := log.Subscribe()
	apply(t, configuration(control, local, newRemote, true, "wrong-token"))
	deadline := time.After(10 * time.Second)
	found := false
	for !found {
		select {
		case event := <-sub:
			if strings.Contains(event.Payload, "[FRPC]") && strings.Contains(event.Payload, "token") && event.LogLevel >= log.WARNING {
				found = true
			}
		case <-deadline:
			log.UnSubscribe(sub)
			t.Fatal("authentication error was not logged")
		}
	}
	log.UnSubscribe(sub)
	check("local SS after auth failure", local)
	apply(t, configuration(control, local, newRemote, false, "wrong-token"))
	frpc.Default.Stop()
	eventually(t, "disabled forwarding", func() error {
		c, err := net.DialTimeout("tcp", address(newRemote), time.Second)
		if err != nil {
			return nil
		}
		c.Close()
		return fmt.Errorf("remote port still open")
	})
	check("local SS after disabling frpc", local)
}
