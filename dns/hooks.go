package dns

import (
	bridge "github.com/metacubex/mihomo/common/hook"
	"github.com/metacubex/mihomo/component/hook"
	icontext "github.com/metacubex/mihomo/context"
	D "github.com/miekg/dns"
)

func withHooks(next handler) handler {
	return func(ctx *icontext.DNSContext, request *D.Msg) (*D.Msg, error) {
		if !hook.Enabled() {
			return next(ctx, request)
		}
		s := hook.NewDNS(request.Question[0].Name)
		if err := s.Host(ctx); err != nil {
			return nil, err
		}
		// DNS transport traffic is not a business-target connection.
		ctx.Context = bridge.Without(ctx.Context)
		response, err := next(ctx, request)
		if err == nil && response != nil && ctx.Type() != icontext.DNSTypeFakeIP {
			err = s.IPs(ctx, hookResponseIPs(request.Question[0].Name, response), "dns", 0)
		}
		if err != nil {
			return nil, err
		}
		return response, nil
	}
}
