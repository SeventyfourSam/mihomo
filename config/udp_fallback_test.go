package config_test

import (
	"testing"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/tunnel"
)

func TestUDPFallbackDefaultParsingAndRollback(t *testing.T) {
	previous := tunnel.UDPFallback()
	t.Cleanup(func() { tunnel.SetUDPFallback(previous) })
	tunnel.SetUDPFallback(true)
	for _, tc := range []struct {
		yaml string
		want bool
	}{
		{"", true},
		{"udp-fallback: true\n", true},
		{"udp-fallback: false\n", false},
	} {
		raw, err := config.UnmarshalRawConfig([]byte(tc.yaml))
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := config.ParseRawConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.General.UDPFallback != tc.want {
			t.Fatalf("%q: got %v, want %v", tc.yaml, parsed.General.UDPFallback, tc.want)
		}
		if !tunnel.UDPFallback() {
			t.Fatal("parsing changed the active runtime setting")
		}
	}
}
