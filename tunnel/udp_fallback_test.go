package tunnel

import (
	"strings"
	"testing"

	C "github.com/metacubex/mihomo/constant"
	RC "github.com/metacubex/mihomo/rules/common"
)

type udpPolicyProxy struct {
	C.Proxy
	name     string
	typ      C.AdapterType
	udp      bool
	child    C.Proxy
	disabled bool
}

func (p *udpPolicyProxy) Name() string                     { return p.name }
func (p *udpPolicyProxy) Type() C.AdapterType              { return p.typ }
func (p *udpPolicyProxy) Unwrap(*C.Metadata, bool) C.Proxy { return p.child }
func (p *udpPolicyProxy) SupportUDP() bool {
	if p.disabled {
		return false
	}
	if p.child != nil {
		return p.child.SupportUDP()
	}
	return p.udp
}

func TestUDPFallbackOnlyControlsUnsupportedMatchedTargets(t *testing.T) {
	oldProxies, oldRules, oldSubRules, oldFallback := proxies, rules, subRules, UDPFallback()
	t.Cleanup(func() { proxies, rules, subRules = oldProxies, oldRules, oldSubRules; SetUDPFallback(oldFallback) })
	unsupported := &udpPolicyProxy{name: "unsupported", typ: C.Http}
	supported := &udpPolicyProxy{name: "supported", typ: C.Direct, udp: true}
	group := &udpPolicyProxy{name: "group", typ: C.Selector, child: unsupported}
	nested := &udpPolicyProxy{name: "nested", typ: C.Selector, child: group}
	disabled := &udpPolicyProxy{name: "disabled", typ: C.Selector, child: supported, disabled: true}
	pass := &udpPolicyProxy{name: "pass", typ: C.Pass, udp: true}
	direct := &udpPolicyProxy{name: "DIRECT", typ: C.Direct, udp: true}
	fallbackRule := RC.NewMatch("DIRECT")
	for _, tc := range []struct {
		name     string
		fallback bool
		network  C.NetWork
		target   C.Proxy
		host     string
		blocked  bool
		want     C.Proxy
	}{
		{"legacy UDP skip", true, C.UDP, unsupported, "target.test", false, direct},
		{"strict UDP block", false, C.UDP, unsupported, "target.test", true, unsupported},
		{"supported UDP", false, C.UDP, supported, "target.test", false, supported},
		{"TCP unchanged", false, C.TCP, unsupported, "target.test", false, unsupported},
		{"nested target blocked", false, C.UDP, nested, "target.test", true, nested},
		{"disabled group blocked", false, C.UDP, disabled, "target.test", true, disabled},
		{"explicit PASS preserved", false, C.UDP, pass, "target.test", false, direct},
		{"normal mismatch preserved", false, C.UDP, unsupported, "other.test", false, direct},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetUDPFallback(tc.fallback)
			proxies = map[string]C.Proxy{"protected": tc.target, "DIRECT": direct}
			matchedRule := RC.NewDomain("target.test", "protected")
			rules, subRules = []C.Rule{matchedRule, fallbackRule}, nil
			proxy, rule, err := match(&C.Metadata{Host: tc.host, NetWork: tc.network}, C.RuleMatchHelper{})
			if proxy != tc.want {
				t.Fatalf("got proxy %v, want %s", proxy, tc.want.Name())
			}
			if tc.blocked {
				if err == nil || rule != matchedRule || !strings.Contains(err.Error(), tc.target.Name()) || !strings.Contains(err.Error(), "udp-fallback: false") {
					t.Fatalf("lost matched rule or failure reason: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
