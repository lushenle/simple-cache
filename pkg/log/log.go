package log

import (
	"io"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Plugin = zapcore.Core

// NewLogger creates a new logger with the given plugin and options.
func NewLogger(plugin zapcore.Core, options ...zap.Option) *zap.Logger {
	return zap.New(plugin, append(DefaultOption(), options...)...)
}

// NewPlugin creates a new plugin with the given writer and level enabler.
func NewPlugin(writer zapcore.WriteSyncer, enabler zapcore.LevelEnabler) Plugin {
	return zapcore.NewCore(DefaultEncoder(), writer, enabler)
}

// NewStdoutPlugin creates a new plugin with the given writer and level enabler.
func NewStdoutPlugin(enabler zapcore.LevelEnabler) Plugin {
	return NewPlugin(zapcore.Lock(zapcore.AddSync(os.Stdout)), enabler)
}

// NewStderrPlugin creates a new plugin with the given writer and level enabler.
func NewStderrPlugin(enabler zapcore.LevelEnabler) Plugin {
	return NewPlugin(zapcore.Lock(zapcore.AddSync(os.Stderr)), enabler)
}

// NewFilePlugin creates a new plugin with the given writer and level enabler.
func NewFilePlugin(filepath string, enabler zapcore.LevelEnabler) (Plugin, io.Closer) {
	writer := DefaultLumberjackLogger()
	writer.Filename = filepath
	return NewPlugin(zapcore.AddSync(writer), enabler), writer
}
