// Package frpc embeds a deliberately small subset of the frp client.
package frpc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"strings"

	v1 "github.com/fatedier/frp/pkg/config/v1"
	"github.com/fatedier/frp/pkg/config/v1/validation"
	frplog "github.com/fatedier/golib/log"
	"go.yaml.in/yaml/v3"
)

// RawConfig retains frpc's camelCase keys inside mihomo's frpc section.
// Only these fields are supported; accepting arbitrary upstream options could
// change process-wide DNS, create a virtual network or start an admin server.
type RawConfig struct {
	Enable        bool            `yaml:"enable" json:"enable"`
	ServerAddr    string          `yaml:"serverAddr" json:"serverAddr"`
	ServerPort    int             `yaml:"serverPort" json:"serverPort"`
	LoginFailExit *bool           `yaml:"loginFailExit" json:"loginFailExit"`
	Auth          AuthConfig      `yaml:"auth" json:"auth"`
	Transport     TransportConfig `yaml:"transport" json:"transport"`
	Log           LogConfig       `yaml:"log" json:"log"`
	Proxies       []ProxyConfig   `yaml:"proxies" json:"proxies"`
}

type AuthConfig struct {
	Method string `yaml:"method" json:"method"`
	Token  string `yaml:"token" json:"token"`
}

type TransportConfig struct {
	Protocol string    `yaml:"protocol" json:"protocol"`
	TLS      TLSConfig `yaml:"tls" json:"tls"`
}

type TLSConfig struct {
	Enable *bool `yaml:"enable" json:"enable"`
}

type LogConfig struct {
	To    string `yaml:"to" json:"to"`
	Level string `yaml:"level" json:"level"`
}

type ProxyConfig struct {
	Name       string `yaml:"name" json:"name"`
	Type       string `yaml:"type" json:"type"`
	LocalIP    string `yaml:"localIP" json:"localIP"`
	LocalPort  int    `yaml:"localPort" json:"localPort"`
	RemotePort int    `yaml:"remotePort" json:"remotePort"`
}

func (c *RawConfig) UnmarshalYAML(node *yaml.Node) error {
	// Check the subtree without re-encoding it, so aliases defined elsewhere in
	// the mihomo YAML continue to work. Errors describe fields, never values.
	type plain RawConfig
	if err := checkFields(node, reflect.TypeOf(plain{}), "frpc", 0); err != nil {
		return err
	}
	var value plain
	if err := node.Decode(&value); err != nil {
		return fmt.Errorf("frpc: invalid YAML structure")
	}
	*c = RawConfig(value)
	return nil
}

func checkFields(n *yaml.Node, t reflect.Type, path string, depth int) error {
	if depth > 64 {
		return fmt.Errorf("%s: YAML nesting or alias cycle is too deep", path)
	}
	if n.Kind == yaml.AliasNode {
		return checkFields(n.Alias, t, path, depth+1)
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: expected a mapping", path)
		}
		fields := make(map[string]reflect.Type)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fields[f.Tag.Get("yaml")] = f.Type
		}
		seen := make(map[string]bool)
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i].Value
			ft, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s.%s: unsupported field", path, key)
			}
			if seen[key] {
				return fmt.Errorf("%s.%s: duplicate field", path, key)
			}
			seen[key] = true
			if err := checkFields(n.Content[i+1], ft, path+"."+key, depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s: expected a list", path)
		}
		for i, child := range n.Content {
			if err := checkFields(child, t.Elem(), fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	default:
		tag := map[reflect.Kind]string{reflect.String: "!!str", reflect.Bool: "!!bool", reflect.Int: "!!int"}[t.Kind()]
		if n.Kind != yaml.ScalarNode || n.Tag != tag {
			return fmt.Errorf("%s: expected %s", path, t.Kind())
		}
	}
	return nil
}

// Config is an immutable, validated snapshot. Its credentials are intentionally
// unexported: neither config API responses nor formatting should expose them.
type Config struct {
	common   v1.ClientCommonConfig
	proxies  []v1.ProxyConfigurer
	identity [32]byte
	logLevel frplog.Level
}

func (c *Config) String() string   { return "frpc.Config{credentials redacted}" }
func (c *Config) GoString() string { return c.String() }

// Parse only validates local configuration. It never dials or resolves frps.
func (raw *RawConfig) Parse() (*Config, error) {
	if raw == nil || !raw.Enable {
		return nil, nil
	}
	r := *raw
	r.Proxies = append([]ProxyConfig(nil), raw.Proxies...)
	if !validServer(r.ServerAddr) {
		return nil, fmt.Errorf("frpc.serverAddr: expected an IP address or hostname without a port")
	}
	if !validPort(r.ServerPort) {
		return nil, fmt.Errorf("frpc.serverPort: expected a port between 1 and 65535")
	}
	if r.LoginFailExit != nil && *r.LoginFailExit {
		return nil, fmt.Errorf("frpc.loginFailExit: must be false (the embedded client always retries)")
	}
	if r.Auth.Method == "" {
		r.Auth.Method = "token"
	}
	if r.Auth.Method != "token" || strings.TrimSpace(r.Auth.Token) == "" {
		return nil, fmt.Errorf("frpc.auth: token authentication with a nonempty token is required")
	}
	if r.Transport.Protocol == "" {
		r.Transport.Protocol = "tcp"
	}
	if r.Transport.Protocol != "tcp" {
		return nil, fmt.Errorf("frpc.transport.protocol: only tcp is supported")
	}
	if r.Log.To == "" {
		r.Log.To = "console"
	}
	if r.Log.To != "console" {
		return nil, fmt.Errorf("frpc.log.to: only console is supported; logs use mihomo's logger")
	}
	if r.Log.Level == "" {
		r.Log.Level = "info"
	}
	level, err := frplog.ParseLevel(r.Log.Level)
	if err != nil {
		return nil, fmt.Errorf("frpc.log.level: expected trace, debug, info, warn or error")
	}
	loginFailExit := false
	c := &Config{common: v1.ClientCommonConfig{
		ServerAddr: r.ServerAddr, ServerPort: r.ServerPort, LoginFailExit: &loginFailExit,
		Auth: v1.AuthClientConfig{Method: v1.AuthMethodToken, Token: r.Auth.Token},
	}, logLevel: level}
	c.common.Transport.Protocol = "tcp"
	tlsEnabled := true
	if r.Transport.TLS.Enable != nil {
		tlsEnabled = *r.Transport.TLS.Enable
	}
	c.common.Transport.TLS.Enable = &tlsEnabled
	c.common.Complete()
	// Complete reads http_proxy. Our connector deliberately ignores that
	// setting, and it must not affect config equality or appear in errors.
	c.common.Transport.ProxyURL = ""
	if len(r.Proxies) == 0 {
		return nil, fmt.Errorf("frpc.proxies: at least one TCP or UDP proxy is required")
	}
	names, ports := map[string]bool{}, map[string]bool{}
	for i, p := range r.Proxies {
		field := fmt.Sprintf("frpc.proxies[%d]", i)
		if strings.TrimSpace(p.Name) == "" || strings.ContainsAny(p.Name, "\r\n\t") || names[p.Name] {
			return nil, fmt.Errorf("%s.name: expected a nonempty, unique name without control characters", field)
		}
		names[p.Name] = true
		if p.Type != "tcp" && p.Type != "udp" {
			return nil, fmt.Errorf("%s.type: only tcp and udp are supported", field)
		}
		if p.LocalIP == "" {
			p.LocalIP = "127.0.0.1"
		}
		ip, err := netip.ParseAddr(p.LocalIP)
		if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
			return nil, fmt.Errorf("%s.localIP: expected a loopback IP address", field)
		}
		if !validPort(p.LocalPort) || !validPort(p.RemotePort) {
			return nil, fmt.Errorf("%s: localPort and remotePort must be between 1 and 65535", field)
		}
		key := fmt.Sprintf("%s/%d", p.Type, p.RemotePort)
		if ports[key] {
			return nil, fmt.Errorf("%s.remotePort: duplicate port for the same protocol", field)
		}
		ports[key] = true
		base := v1.ProxyBaseConfig{Name: p.Name, Type: p.Type,
			ProxyBackend: v1.ProxyBackend{LocalIP: ip.String(), LocalPort: p.LocalPort}}
		var proxy v1.ProxyConfigurer
		if p.Type == "tcp" {
			proxy = &v1.TCPProxyConfig{ProxyBaseConfig: base, RemotePort: p.RemotePort}
		} else {
			proxy = &v1.UDPProxyConfig{ProxyBaseConfig: base, RemotePort: p.RemotePort}
		}
		proxy.Complete("")
		c.proxies = append(c.proxies, proxy)
	}
	if _, err := validation.ValidateAllClientConfig(&c.common, c.proxies, nil); err != nil {
		return nil, fmt.Errorf("frpc: %w", err)
	}
	identity, err := json.Marshal(struct {
		Common  v1.ClientCommonConfig
		Proxies []v1.ProxyConfigurer
		Level   frplog.Level
	}{c.common, c.proxies, c.logLevel})
	if err != nil {
		return nil, fmt.Errorf("frpc: cannot normalize configuration")
	}
	c.identity = sha256.Sum256(identity)
	return c, nil
}

func validPort(port int) bool { return port > 0 && port <= 65535 }

func validServer(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
