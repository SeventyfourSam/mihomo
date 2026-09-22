package config_test

import (
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
	_ "github.com/metacubex/mihomo/hub/executor" // supplies temporaryUpdateGeneral's linkname target
)

func TestFRPCParsedBeforeOtherConfig(t *testing.T) {
	// A valid hostname is checked syntactically only, including in -t.
	data := []byte(`frpc:
  enable: true
  serverAddr: unreachable.example.test
  serverPort: 8443
  auth: {token: local-test-only}
  proxies:
    - {name: ss-tcp, type: tcp, localPort: 18885, remotePort: 8885}
`)
	raw, err := config.UnmarshalRawConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.FRPC.Parse(); err != nil {
		t.Fatal(err)
	}
	raw.FRPC.ServerPort = -1
	if _, err := config.ParseRawConfig(raw); err == nil || !strings.Contains(err.Error(), "frpc.serverPort") {
		t.Fatal("invalid frpc config was not rejected before applying configuration", err)
	}
	if _, err := config.UnmarshalRawConfig([]byte("frpc:\n  enable: true\n  dnsServer: 8.8.8.8\n")); err == nil {
		t.Fatal("unknown frpc option was ignored")
	}
	// Preserve mihomo's existing permissive behavior outside this subtree.
	if _, err := config.UnmarshalRawConfig([]byte("unrelated-custom-field: true\n")); err != nil {
		t.Fatal(err)
	}
}
