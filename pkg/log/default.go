package log

import (
	"github.com/lushenle/simple-cache/pkg/common"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// DefaultEncoderConfig returns the default encoder configuration for the logger.
func DefaultEncoderConfig() zapcore.EncoderConfig {
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return encoderConfig
}

// DefaultEncoder returns the default encoder for the logger.
func DefaultEncoder() zapcore.Encoder {
	return zapcore.NewJSONEncoder(DefaultEncoderConfig())
}

// DefaultOption returns the default options for the logger.
func DefaultOption() []zap.Option {
	var stackTraceLevel zap.LevelEnablerFunc = func(level zapcore.Level) bool {
		return level >= zapcore.DPanicLevel
	}

	return []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(stackTraceLevel),
	}
}

// DefaultLumberjackLogger Compresses every 200mb, does not rotate by time, retained for 7 days
func DefaultLumberjackLogger() *lumberjack.Logger {
	return &lumberjack.Logger{
		MaxSize:   common.MaxLogFileSize,
		LocalTime: true,
		Compress:  true,
		MaxAge:    common.MaxLogFileAge,
	}
}
