package provider

import C "github.com/metacubex/mihomo/constant"

// HookRules exposes the current immutable rule slice without changing routing.
func (c *classicalStrategy) HookRules() []C.Rule { return c.rules }

func (rs *RuleSet) HookSource() bool { return rs.isSrc }
