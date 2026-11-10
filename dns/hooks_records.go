package dns

import (
	"net"
	"net/netip"

	D "github.com/miekg/dns"
)

// hookResponseIPs observes addresses exposed to this DNS client without
// changing the resolver's A/AAAA lookup or fallback semantics.
func hookResponseIPs(question string, response *D.Msg) (ips []netip.Addr) {
	owners := make(map[string][]D.RR)
	for _, section := range [][]D.RR{response.Answer, response.Extra} {
		for _, rr := range section {
			if rr.Header().Class == D.ClassINET {
				name := D.CanonicalName(rr.Header().Name)
				owners[name] = append(owners[name], rr)
			}
		}
	}
	seen := make(map[string]bool)
	var names []string
	addName := func(name string) {
		if name == "" {
			return
		}
		name = D.CanonicalName(name)
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	addIP := func(ip net.IP) {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			ips = append(ips, addr)
		}
	}
	addName(question)
	for i := 0; i < len(names); i++ {
		records := owners[names[i]]
		var httpsAlias, svcbAlias bool
		for _, rr := range records {
			switch rr := rr.(type) {
			case *D.HTTPS:
				httpsAlias = httpsAlias || rr.Priority == 0
			case *D.SVCB:
				svcbAlias = svcbAlias || rr.Priority == 0
			}
		}
		for _, rr := range records {
			var binding *D.SVCB
			var alias bool
			switch rr := rr.(type) {
			case *D.A:
				addIP(rr.A)
			case *D.AAAA:
				addIP(rr.AAAA)
			case *D.CNAME:
				addName(rr.Target)
			case *D.HTTPS:
				binding, alias = &rr.SVCB, httpsAlias
			case *D.SVCB:
				binding, alias = rr, svcbAlias
			}
			if binding == nil || (binding.Priority != 0 && alias) {
				continue
			}
			// Only follow names advertised by the queried service, never NS glue
			// or unrelated Additional records. A "." target adds no other owner.
			if binding.Target != "." {
				addName(binding.Target)
			}
			if binding.Priority == 0 {
				continue // AliasMode ignores SvcParams, including address hints.
			}
			for _, param := range binding.Value {
				switch param := param.(type) {
				case *D.SVCBIPv4Hint:
					for _, ip := range param.Hint {
						addIP(ip)
					}
				case *D.SVCBIPv6Hint:
					for _, ip := range param.Hint {
						addIP(ip)
					}
				}
			}
		}
	}
	return
}
