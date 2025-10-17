// file: internal/lifecycle/application.go

// Package lifecycle provides application lifecycle management including
// graceful shutdown and runtime reloading via SIGHUP signal.
package lifecycle

import "context"

// Application represents a runnable application that supports graceful
// shutdown and runtime reloading. Both rule-router and http-gateway
// implement this interface.
type Application interface {
	// Run starts the application and blocks until the context is cancelled.
	// It should handle all application logic including starting servers,
	// processing messages, and monitoring for shutdown signals.
	//
	// Returns an error if the application encounters a fatal error during
	// operation. Normal shutdown should return nil.
	Run(ctx context.Context) error

	// Close gracefully shuts down the application, releasing all resources.
	// This includes:
	// - Draining in-flight messages
	// - Closing NATS connections
	// - Shutting down HTTP servers
	// - Stopping background goroutines
	// - Syncing logs and metrics
	//
	// Close should be idempotent and safe to call multiple times.
	Close() error
}
