package logger

import (
	"log/slog"
	"os"
)

var Log *slog.Logger

func InitLogger() {
	Log = slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

func Fatal(msg string, err error) {
	Log.Error(msg, "error", err)
	os.Exit(1)
}
