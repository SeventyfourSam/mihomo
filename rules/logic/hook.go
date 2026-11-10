package logic

import C "github.com/metacubex/mihomo/constant"

// HookRules exposes read-only children for matching partial hook metadata.
// Routing continues to use Match and its existing boolean semantics.
func (logic *Logic) HookRules() []C.Rule { return logic.rules }
