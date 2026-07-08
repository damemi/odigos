// Package log provides subsystem-scoped loggers for connector runtime code.
package log

import (
	commonlogger "github.com/odigos-io/odigos/common/logger"
	"go.uber.org/zap"
)

// Logger is a subsystem-scoped logger. It resolves the underlying zap logger on each call so
// package-level vars remain valid after commonlogger.Init runs in main.
type Logger struct {
	subsystem string
}

// Subsystem returns a structured logger tagged with the given subsystem name.
func Subsystem(name string) *Logger {
	return &Logger{subsystem: name}
}

func (l *Logger) sugar() *zap.SugaredLogger {
	zl := commonlogger.Logger()
	if zl == nil {
		return zap.NewNop().Sugar()
	}
	// Skip this wrapper frame so caller annotations point at the real call site.
	s := zl.WithOptions(zap.AddCallerSkip(1)).Sugar()
	if l != nil && l.subsystem != "" {
		s = s.With("subsystem", l.subsystem)
	}
	return s
}

func (l *Logger) Info(msg string, keysAndValues ...interface{}) {
	l.sugar().Infow(msg, keysAndValues...)
}

func (l *Logger) Error(msg string, keysAndValues ...interface{}) {
	l.sugar().Errorw(msg, keysAndValues...)
}

func (l *Logger) Warn(msg string, keysAndValues ...interface{}) {
	l.sugar().Warnw(msg, keysAndValues...)
}

func (l *Logger) Debug(msg string, keysAndValues ...interface{}) {
	l.sugar().Debugw(msg, keysAndValues...)
}
