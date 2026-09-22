package frpc

import (
	"strings"
	"sync/atomic"
	"time"

	frplog "github.com/fatedier/frp/pkg/util/log"
	goliblog "github.com/fatedier/golib/log"
	"github.com/metacubex/mihomo/log"
)

type logSettings struct {
	level goliblog.Level
	token string
}

type logWriter struct{ settings atomic.Pointer[logSettings] }

var output logWriter

func init() {
	output.settings.Store(&logSettings{level: goliblog.InfoLevel})
	// Install once before any service starts. Reloads update only our atomic
	// filter, never the upstream global Logger while workers are using it.
	frplog.Logger = goliblog.New(goliblog.WithLevel(goliblog.TraceLevel),
		goliblog.WithCaller(false), goliblog.WithOutput(&output))
}

func (w *logWriter) Write(p []byte) (int, error) {
	return w.WriteLog(p, goliblog.InfoLevel, time.Time{})
}

func (w *logWriter) WriteLog(p []byte, level goliblog.Level, _ time.Time) (int, error) {
	s := w.settings.Load()
	if level < s.level {
		return len(p), nil
	}
	text := strings.TrimSpace(string(p))
	// golib prefixes each entry with a 23-byte timestamp and a level. mihomo
	// supplies those itself, while run IDs and proxy names remain in the text.
	if len(p) >= 28 && p[23] == ' ' && p[24] == '[' && p[26] == ']' {
		text = strings.TrimSpace(string(p[28:]))
	}
	if s.token != "" {
		text = strings.ReplaceAll(text, s.token, "[REDACTED]")
	}
	switch level {
	case goliblog.TraceLevel, goliblog.DebugLevel:
		log.Debugln("[FRPC] %s", text)
	case goliblog.InfoLevel:
		log.Infoln("[FRPC] %s", text)
	case goliblog.WarnLevel:
		log.Warnln("[FRPC] %s", text)
	default:
		log.Errorln("[FRPC] %s", text)
	}
	return len(p), nil
}
