package frpc

import (
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const validYAML = `enable: true
serverAddr: frps.example.test
serverPort: 8443
auth:
  method: token
  token: test-token-never-print
transport:
  tls:
    enable: true
proxies:
  - name: ss-tcp
    type: tcp
    localIP: 127.0.0.1
    localPort: 18885
    remotePort: 8885
  - name: ss-udp
    type: udp
    localPort: 18885
    remotePort: 8885
`

func parseYAML(value string) (*Config, error) {
	var raw RawConfig
	if err := yaml.Unmarshal([]byte(value), &raw); err != nil {
		return nil, err
	}
	return raw.Parse()
}

func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"unknown", "serverPort:", "serverProt:", "unsupported field"},
		{"dns override", "auth:", "dnsServer: 1.1.1.1\nauth:", "unsupported field"},
		{"plugin", "    type: tcp", "    type: tcp\n    plugin: {type: socks5}", "unsupported field"},
		{"admin", "auth:", "webServer: {port: 7400}\nauth:", "unsupported field"},
		{"virtual network", "auth:", "virtualNet: {address: 10.0.0.1/24}\nauth:", "unsupported field"},
		{"duplicate field", "serverPort: 8443", "serverPort: 8443\nserverPort: 8443", "duplicate field"},
		{"bad scalar", "serverPort: 8443", "serverPort: test-token-never-print", "expected int"},
		{"bad server", "frps.example.test", "https://example.test:8443", "serverAddr"},
		{"server port", "serverPort: 8443", "serverPort: 0", "serverPort"},
		{"remote port", "remotePort: 8885", "remotePort: 65536", "remotePort"},
		{"duplicate name", "name: ss-udp", "name: ss-tcp", "unique name"},
		{"duplicate port", "type: udp", "type: tcp", "duplicate port"},
		{"unsupported proxy", "type: udp", "type: http", "only tcp and udp"},
		{"backend hostname", "127.0.0.1", "localhost", "loopback IP"},
		{"backend nonlocal", "127.0.0.1", "192.168.1.1", "loopback IP"},
		{"empty token", "token: test-token-never-print", "token: ''", "nonempty token"},
		{"auth method", "method: token", "method: oidc", "token authentication"},
		{"exit on fail", "auth:", "loginFailExit: true\nauth:", "must be false"},
		{"log file", "auth:", "log: {to: frpc.log}\nauth:", "only console"},
		{"log level", "auth:", "log: {level: fatal}\nauth:", "log.level"},
		{"quic", "transport:", "transport:\n  protocol: quic", "only tcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseYAML(strings.ReplaceAll(validYAML, tc.from, tc.to))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "test-token-never-print") {
				t.Fatal("configuration error exposed a value")
			}
		})
	}
}

func TestConfigDefaultsAndAliases(t *testing.T) {
	a, err := parseYAML(validYAML)
	if err != nil {
		t.Fatal(err)
	}
	// Same numeric remote port is valid for one TCP and one UDP mapping.
	if len(a.proxies) != 2 || *a.common.LoginFailExit || !*a.common.Transport.TLS.Enable {
		t.Fatal("incorrect defaults")
	}
	for _, value := range []string{fmt.Sprintf("%v", a), fmt.Sprintf("%+v", a), fmt.Sprintf("%#v", a)} {
		if strings.Contains(value, "test-token-never-print") {
			t.Fatal("config formatting exposed credentials")
		}
	}
	b, err := parseYAML(validYAML + "loginFailExit: false\nlog: {to: console, level: info}\n")
	if err != nil || !equalConfig(a, b) {
		t.Fatal("explicit defaults changed config identity", err)
	}
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	c, err := parseYAML(validYAML)
	if err != nil || !equalConfig(a, c) || c.common.Transport.ProxyURL != "" {
		t.Fatal("environment proxy changed config", err)
	}
	var doc struct {
		Port   int       `yaml:"port"`
		Client RawConfig `yaml:"frpc"`
	}
	input := "port: &port 8443\nfrpc:\n  " + strings.ReplaceAll(strings.ReplaceAll(validYAML, "serverPort: 8443", "serverPort: *port"), "\n", "\n  ")
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Client.Parse(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []*RawConfig{nil, {Enable: false}} {
		cfg, err := raw.Parse()
		if cfg != nil || err != nil {
			t.Fatal("disabled config should not require server settings")
		}
	}
}
