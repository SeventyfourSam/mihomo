// Package hook implements configurable HOST/IP notifications and send barriers.
package hook

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	R "github.com/metacubex/mihomo/rules"
	RC "github.com/metacubex/mihomo/rules/common"
)

type ProviderConfig struct {
	Type            string            `yaml:"type" json:"type"`
	Mode            string            `yaml:"mode" json:"mode"`
	URL             string            `yaml:"url" json:"url"`
	Headers         map[string]string `yaml:"headers" json:"headers"`
	Command         string            `yaml:"command" json:"command"`
	Args            []string          `yaml:"args" json:"args"`
	Timeout         time.Duration     `yaml:"timeout" json:"timeout"`
	SuccessStatus   int               `yaml:"success-status" json:"success-status"`
	OnError         string            `yaml:"on-error" json:"on-error"`
	ConfirmationTTL time.Duration     `yaml:"confirmation-ttl" json:"confirmation-ttl"`
}

type RuleConfig struct {
	Event     string   `yaml:"event" json:"event"`
	Match     string   `yaml:"match" json:"match"`
	Providers []string `yaml:"providers" json:"providers"`
}

type rule struct {
	event     string
	match     C.Rule
	providers []string
}

type Config struct {
	providers   map[string]ProviderConfig
	rules       []rule
	ruleSets    map[string]P.RuleProvider
	needProcess bool
}

func Parse(providers map[string]ProviderConfig, hooks []RuleConfig, ruleSets map[string]P.RuleProvider) (*Config, error) {
	if len(hooks) == 0 && len(providers) == 0 {
		return nil, nil
	}
	cfg := &Config{providers: make(map[string]ProviderConfig), ruleSets: make(map[string]P.RuleProvider)}
	for name, provider := range ruleSets {
		cfg.ruleSets[name] = provider
	}
	for name, p := range providers {
		if name == "" {
			return nil, fmt.Errorf("hook provider name cannot be empty")
		}
		if p.Mode == "" {
			p.Mode = "async"
		}
		if p.Mode != "sync" && p.Mode != "async" {
			return nil, fmt.Errorf("hook provider %q: mode must be sync or async", name)
		}
		if p.Timeout == 0 {
			p.Timeout = 2 * time.Second
		}
		if p.ConfirmationTTL == 0 {
			p.ConfirmationTTL = 30 * time.Second
		}
		if p.Timeout < 0 || p.ConfirmationTTL < 0 {
			return nil, fmt.Errorf("hook provider %q: durations must be positive", name)
		}
		if p.OnError == "" {
			p.OnError = "block"
			if p.Mode == "async" {
				p.OnError = "continue"
			}
		}
		if p.OnError != "block" && p.OnError != "continue" || p.Mode == "async" && p.OnError == "block" {
			return nil, fmt.Errorf("hook provider %q: async requires on-error continue; sync accepts block or continue", name)
		}
		switch p.Type {
		case "api":
			u, err := url.Parse(p.URL)
			if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
				return nil, fmt.Errorf("hook provider %q: url must be absolute HTTP(S)", name)
			}
			if p.SuccessStatus == 0 {
				p.SuccessStatus = 200
			}
			if p.SuccessStatus < 100 || p.SuccessStatus > 599 {
				return nil, fmt.Errorf("hook provider %q: invalid success-status", name)
			}
		case "script":
			command, err := exec.LookPath(p.Command)
			if err != nil {
				return nil, fmt.Errorf("hook provider %q: command: %w", name, err)
			}
			p.Command = command
		default:
			return nil, fmt.Errorf("hook provider %q: type must be api or script", name)
		}
		p.Args = append([]string(nil), p.Args...)
		if p.Headers != nil {
			headers := make(map[string]string, len(p.Headers))
			for key, value := range p.Headers {
				headers[key] = value
			}
			p.Headers = headers
		}
		cfg.providers[name] = p
	}
	for i, h := range hooks {
		if h.Event != "HOST" && h.Event != "IP" {
			return nil, fmt.Errorf("hooks[%d]: event must be HOST or IP", i)
		}
		if strings.TrimSpace(h.Match) == "" || len(h.Providers) == 0 {
			return nil, fmt.Errorf("hooks[%d]: match and providers are required", i)
		}
		tp, payload, _, params := RC.ParseRulePayload(h.Match, false)
		parsed, err := R.ParseRule(tp, payload, "", params, nil)
		if err != nil {
			return nil, fmt.Errorf("hooks[%d]: %w", i, err)
		}
		if err = cfg.validateRule(parsed); err != nil {
			return nil, fmt.Errorf("hooks[%d]: %w", i, err)
		}
		for _, name := range h.Providers {
			if _, ok := cfg.providers[name]; !ok {
				return nil, fmt.Errorf("hooks[%d]: unknown provider %q", i, name)
			}
		}
		cfg.rules = append(cfg.rules, rule{h.Event, parsed, append([]string(nil), h.Providers...)})
	}
	return cfg, nil
}

func (c *Config) validateRule(r C.Rule) error {
	switch r.RuleType() {
	case C.SubRules:
		return fmt.Errorf("SUB-RULE is a routing action, not a hook condition")
	case C.RuleSet:
		p, ok := c.ruleSets[r.Payload()]
		if !ok {
			return fmt.Errorf("unknown rule-set %q", r.Payload())
		}
		c.needProcess = c.needProcess || p.Behavior() == P.Classical
	case C.ProcessName, C.ProcessPath, C.ProcessNameRegex, C.ProcessPathRegex, C.ProcessNameWildcard, C.ProcessPathWildcard, C.Uid:
		c.needProcess = true
	}
	if children, ok := r.(interface{ HookRules() []C.Rule }); ok {
		for _, child := range children.HookRules() {
			if err := c.validateRule(child); err != nil {
				return err
			}
		}
	}
	return nil
}
