package hook

import (
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

type truth uint8

const (
	unknown truth = iota
	no
	yes
)

func (c *Config) matches(r C.Rule, m *C.Metadata, dns bool) truth {
	if children, ok := r.(interface{ HookRules() []C.Rule }); ok {
		return c.combine(r.RuleType(), children.HookRules(), m, dns)
	}
	if r.RuleType() == C.RuleSet {
		p := c.ruleSets[r.Payload()]
		if p == nil {
			return unknown
		}
		copyM := *m
		if src, ok := r.(interface{ HookSource() bool }); ok && src.HookSource() {
			copyM.SwapSrcDst()
		}
		switch p.Behavior() {
		case P.Domain:
			if copyM.RuleHost() == "" {
				return unknown
			}
		case P.IPCIDR:
			if !copyM.DstIP.IsValid() {
				return unknown
			}
		case P.Classical:
			if rules, ok := p.Strategy().(interface{ HookRules() []C.Rule }); ok {
				return c.combine(C.OR, rules.HookRules(), &copyM, dns)
			}
			return unknown
		}
		return boolean(p.Match(&copyM, C.RuleMatchHelper{}))
	}
	available := true
	switch r.RuleType() {
	case C.Domain, C.DomainSuffix, C.DomainKeyword, C.DomainRegex, C.DomainWildcard, C.GEOSITE:
		available = m.RuleHost() != ""
	case C.IPCIDR, C.GEOIP, C.IPASN, C.IPSuffix:
		available = m.DstIP.IsValid()
	case C.SrcIPCIDR, C.SrcGEOIP, C.SrcIPASN, C.SrcIPSuffix:
		available = m.SrcIP.IsValid()
	case C.ProcessName, C.ProcessNameRegex, C.ProcessNameWildcard:
		available = m.Process != ""
	case C.ProcessPath, C.ProcessPathRegex, C.ProcessPathWildcard:
		available = m.ProcessPath != ""
	case C.Uid:
		available = m.Uid != 0
	case C.SrcPort:
		available = m.SrcPort != 0
	case C.DstPort:
		available = m.DstPort != 0
	case C.InPort:
		available = m.InPort != 0
	case C.Network:
		available = m.NetWork == C.TCP || m.NetWork == C.UDP
	case C.InType, C.DSCP:
		available = !dns
	case C.InName:
		available = !dns && m.InName != ""
	case C.InUser:
		available = !dns && m.InUser != ""
	case C.RematchName:
		available = !dns && m.RematchName != ""
	}
	if !available {
		return unknown
	}
	copyM := *m
	copyM.SrcGeoIP, copyM.DstGeoIP = nil, nil
	copyM.SrcIPASN, copyM.DstIPASN = "", ""
	matched, _ := r.Match(&copyM, C.RuleMatchHelper{}) // never resolve DNS or guess a process
	return boolean(matched)
}

func (c *Config) combine(op C.RuleType, rules []C.Rule, m *C.Metadata, dns bool) truth {
	missing := false
	for _, r := range rules {
		v := c.matches(r, m, dns)
		switch op {
		case C.NOT:
			if v == unknown {
				return unknown
			}
			return boolean(v == no)
		case C.OR:
			if v == yes {
				return yes
			}
		case C.AND:
			if v == no {
				return no
			}
		}
		missing = missing || v == unknown
	}
	if missing {
		return unknown
	}
	return boolean(op == C.AND)
}

func boolean(v bool) truth {
	if v {
		return yes
	}
	return no
}
