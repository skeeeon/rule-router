// file: internal/logger/logger.go

//go:build !js

package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"rule-router/config"

	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
)

// Logger wraps slog.Logger with a zap backend for structured logging.
// Use slog's native key-value API: logger.Info("msg", "key", val, "key2", val2)
type Logger struct {
	*slog.Logger
	syncer interface{ Sync() error }
}

// NewLogger creates a new Logger backed by zap with the given configuration.
func NewLogger(cfg *config.LogConfig) (*Logger, error) {
	if cfg == nil {
		return nil, fmt.Errorf("logger config is nil")
	}

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

	zapCfg.EncoderConfig.TimeKey = "timestamp"
	zapCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	zapCfg.EncoderConfig.EncodeDuration = zapcore.StringDurationEncoder
	zapCfg.EncoderConfig.StacktraceKey = "stacktrace"

	zapLogger, err := zapCfg.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}

	handler := zapslog.NewHandler(zapLogger.Core(),
		zapslog.WithCaller(true),
		zapslog.WithCallerSkip(1),
		zapslog.AddStacktraceAt(slog.LevelError),
	)

	return &Logger{
		Logger: slog.New(handler),
		syncer: zapLogger,
	}, nil
}

// Fatal logs a message at Error level and exits the process.
// slog does not define a Fatal level, so this logs at Error and calls os.Exit(1).
func (l *Logger) Fatal(msg string, args ...any) {
	l.Logger.Error(msg, args...)
	_ = l.syncer.Sync()
	os.Exit(1)
}

// Debug logs a message at Debug level.
// Short-circuits when debug logging is disabled to avoid allocating args.
func (l *Logger) Debug(msg string, args ...any) {
	if !l.Logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	l.Logger.Debug(msg, args...)
}

// With returns a new Logger with the given key-value pairs as persistent fields.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{
		Logger: l.Logger.With(args...),
		syncer: l.syncer,
	}
}

// Sync flushes any buffered log entries from the underlying zap core.
func (l *Logger) Sync() error {
	return l.syncer.Sync()
}

// NewNopLogger creates a logger that discards all log output. Useful for tests.
func NewNopLogger() *Logger {
	return &Logger{
		Logger: slog.New(slog.DiscardHandler),
		syncer: noopSyncer{},
	}
}

// NewBootstrapLogger creates a minimal JSON logger writing to stderr.
// Use this in main() before the full configuration is available.
func NewBootstrapLogger() *Logger {
	zapLogger, _ := zap.NewProduction()

	handler := zapslog.NewHandler(zapLogger.Core(),
		zapslog.WithCaller(true),
		zapslog.WithCallerSkip(1),
	)

	return &Logger{
		Logger: slog.New(handler),
		syncer: zapLogger,
	}
}

type noopSyncer struct{}

func (noopSyncer) Sync() error { return nil }
