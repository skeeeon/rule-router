// Package logger provides the structured logger used across rule-router.
//
// The API is slog-style — a message followed by alternating key/value pairs —
// backed by zap in normal builds and by slog alone under GOOS=js, where zap is
// too heavy to link into the WASM binary. Callers see the same *Logger either
// way; With derives a child logger carrying additional fields.
package logger
