package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
)

func TestHooksYAMLValidation(t *testing.T) {
	text := []byte(`hook-providers:
  TargetIp:
    type: api
    mode: sync
    url: http://127.0.0.1:1234/hooks/ip
    timeout: 2s
    confirmation-ttl: 30s
hooks:
  - event: IP
    match: DOMAIN-SUFFIX,example.test
    providers: [TargetIp]
`)
	raw, err := config.UnmarshalRawConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := config.ParseRawConfig(raw)
	if err != nil || parsed.Hooks == nil {
		t.Fatal("hook config not parsed", err)
	}
	raw.Hooks[0].Providers[0] = "missing"
	if _, err := config.ParseRawConfig(raw); err == nil {
		t.Fatal("unknown hook provider accepted")
	}
}

func TestDocumentedHooksConfiguration(t *testing.T) {
	document, err := os.ReadFile("../docs/hooks.md")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(document), "```yaml\n")
	if !ok {
		t.Fatal("missing example")
	}
	text, _, ok := strings.Cut(body, "\n```")
	if !ok {
		t.Fatal("incomplete example")
	}
	raw, err := config.UnmarshalRawConfig([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseRawConfig(raw); err != nil {
		t.Fatal("documented example is invalid", err)
	}
}
