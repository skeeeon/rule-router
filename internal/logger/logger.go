//file: internal/logger/logger.go

package logger

import (
	"fmt"
	"rule-router/config"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger wraps zap.Logger to provide application-specific logging
type Logger struct {
	*zap.Logger
}

// NewLogger creates a new logger instance
func NewLogger(cfg *config.LogConfig) (*Logger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("logger config is nil")
	}

	// Parse log level
	var level zapcore.Level
	switch cfg.Level {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
	default:
		level = zap.InfoLevel
	}

	// Create zap config
	zapCfg := zap.Config{
		Level:       zap.NewAtomicLevelAt(level),
		Development: false,
		Sampling: &zap.SamplingConfig{
			Initial:    100,
			Thereafter: 100,
		},
		Encoding:         cfg.Encoding,
		EncoderConfig:    zap.NewProductionEncoderConfig(),
		OutputPaths:      []string{cfg.OutputPath},
		ErrorOutputPaths: []string{cfg.OutputPath},
	}

	// Customize encoder config
	zapCfg.EncoderConfig.TimeKey = "timestamp"
	zapCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	zapCfg.EncoderConfig.EncodeDuration = zapcore.StringDurationEncoder
	zapCfg.EncoderConfig.StacktraceKey = "stacktrace"

	logger, err := zapCfg.Build(
		zap.AddCallerSkip(1),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	return &Logger{Logger: logger}, nil
}

// Fatal logs a message at Fatal level and exits
func (l *Logger) Fatal(msg string, args ...interface{}) {
	fields := argsToFields(args...)
	l.Logger.Fatal(msg, fields...)
}

// Error logs a message at Error level
func (l *Logger) Error(msg string, args ...interface{}) {
	fields := argsToFields(args...)
	l.Logger.Error(msg, fields...)
}

// Warn logs a message at Warn level
func (l *Logger) Warn(msg string, args ...interface{}) {
	fields := argsToFields(args...)
	l.Logger.Warn(msg, fields...)
}

// Info logs a message at Info level
func (l *Logger) Info(msg string, args ...interface{}) {
	fields := argsToFields(args...)
	l.Logger.Info(msg, fields...)
}

// Debug logs a message at Debug level
func (l *Logger) Debug(msg string, args ...interface{}) {
	fields := argsToFields(args...)
	l.Logger.Debug(msg, fields...)
}

// Sync flushes any buffered log entries
func (l *Logger) Sync() error {
	return l.Logger.Sync()
}

// argsToFields converts variadic args to zap fields
func argsToFields(args ...interface{}) []zap.Field {
	fields := make([]zap.Field, 0, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		if i+1 < len(args) {
			key, ok := args[i].(string)
			if !ok {
				continue
			}
			fields = append(fields, zap.Any(key, args[i+1]))
		}
	}
	return fields
}
