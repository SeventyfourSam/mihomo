package tunnel

import "github.com/metacubex/mihomo/common/atomic"

var udpFallback = atomic.NewBool(true)

// UDPFallback controls skipping a matched rule whose target lacks UDP support.
// It does not change a proxy group's own selection or connection retry policy.
func UDPFallback() bool { return udpFallback.Load() }

func SetUDPFallback(enabled bool) { udpFallback.Store(enabled) }
