// file: internal/logger/logger_wasm.go
// Lightweight logger for WASM builds — no zap, no viper, no config dependencies.

//go:build js && wasm

package logger

import (
	"context"
	"log/slog"
	"os"
)

// Logger wraps slog.Logger with structured logging.
type Logger struct {
	*slog.Logger
	syncer interface{ Sync() error }
}

// NewNopLogger creates a logger that discards all log output.
func NewNopLogger() *Logger {
	return &Logger{
		Logger: slog.New(slog.DiscardHandler),
		syncer: noopSyncer{},
	}
}

// Fatal logs at Error level and exits.
func (l *Logger) Fatal(msg string, args ...any) {
	l.Logger.Error(msg, args...)
	_ = l.syncer.Sync()
	os.Exit(1)
}

// Debug short-circuits when disabled.
func (l *Logger) Debug(msg string, args ...any) {
	if !l.Logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	l.Logger.Debug(msg, args...)
}

// With returns a new Logger with persistent fields.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{
		Logger: l.Logger.With(args...),
		syncer: l.syncer,
	}
}

// Sync flushes any buffered log entries.
func (l *Logger) Sync() error {
	return l.syncer.Sync()
}

type noopSyncer struct{}

func (noopSyncer) Sync() error { return nil }
