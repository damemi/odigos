package log

import (
	"os"
	"strings"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Init configures process logging for connector runtimes. Defaults to debug when ODIGOS_LOG_LEVEL is
// unset. Console format is the default; set ODIGOS_LOG_FORMAT=json for structured JSON output.
func Init(level, component string) {
	if strings.TrimSpace(level) == "" {
		level = "debug"
	}
	if os.Getenv("ODIGOS_LOG_FORMAT") == "json" {
		commonlogger.Init(level, component)
		return
	}
	initConsole(level, component)
}

func initConsole(levelStr, component string) {
	atom := commonlogger.AtomicLevel()
	atom.SetLevel(commonlogger.ParseLevel(levelStr))

	encCfg := zap.NewDevelopmentEncoderConfig()
	encCfg.EncodeLevel = zapcore.CapitalLevelEncoder
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encCfg),
		zapcore.AddSync(os.Stdout),
		atom,
	)
	zl := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	if component != "" {
		zl = zl.With(zap.String("component", component))
	}
	commonlogger.UpdateInstance(zl)
}
