// file: internal/lifecycle/lifecycle.go

package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rule-router/internal/logger"
)

// RunWithReload runs an application with automatic reload support on SIGHUP.
// It handles the complete lifecycle including:
// - Initial startup
// - Signal handling (SIGTERM, SIGINT, SIGHUP)
// - Graceful shutdown on SIGTERM/SIGINT
// - Reload on SIGHUP
// - Error propagation
//
// The createApp function is called on initial startup and each reload to
// create a fresh application instance. If createApp returns an error,
// the process exits.
//
// Example usage:
//
//	createApp := func() (Application, error) {
//	    return app.NewRouterApp(cfg, rulesPath)
//	}
//	err := lifecycle.RunWithReload(createApp, logger)
func RunWithReload(
	createApp func() (Application, error),
	log *logger.Logger,
) error {
	reloadCount := 0

	for {
		// Log reload attempt (skip for initial startup)
		if reloadCount > 0 {
			log.Info("initiating application reload",
				"reloadCount", reloadCount)
		}

		// Create signal channels for shutdown and reload
		shutdownSig := make(chan os.Signal, 1)
		reloadSig := make(chan os.Signal, 1)

		signal.Notify(shutdownSig, os.Interrupt, syscall.SIGTERM)
		signal.Notify(reloadSig, syscall.SIGHUP)

		// Create application instance
		startTime := time.Now()
		application, err := createApp()
		if err != nil {
			// Cleanup signal handlers before returning
			signal.Stop(shutdownSig)
			signal.Stop(reloadSig)
			close(shutdownSig)
			close(reloadSig)

			if reloadCount > 0 {
				log.Error("FATAL: failed to reload application",
					"reloadCount", reloadCount,
					"error", err)
				log.Info("process will exit - fix the error and restart")
			}
			return fmt.Errorf("failed to create application: %w", err)
		}

		if reloadCount > 0 {
			duration := time.Since(startTime)
			log.Info("application reload completed successfully",
				"reloadCount", reloadCount,
				"duration", duration)
		}

		// Run application in goroutine so we can handle signals
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- application.Run(ctx)
		}()

		// Wait for signal or application error
		var shouldReload bool
		var runErr error

		select {
		case sig := <-shutdownSig:
			log.Info("shutdown signal received - initiating graceful shutdown",
				"signal", sig)
			shouldReload = false

		case <-reloadSig:
			log.Info("SIGHUP received - initiating reload")
			log.Info("draining in-flight messages and closing connections")
			shouldReload = true
			reloadCount++

		case runErr = <-errCh:
			log.Error("application stopped with error",
				"error", runErr,
				"reloadCount", reloadCount)
			shouldReload = false
		}

		// Cancel context to stop application
		cancel()

		// Stop listening for signals and cleanup channels
		signal.Stop(shutdownSig)
		signal.Stop(reloadSig)
		close(shutdownSig)
		close(reloadSig)

		// Gracefully close application
		log.Info("closing application")
		closeStart := time.Now()
		if closeErr := application.Close(); closeErr != nil {
			log.Error("error during application close",
				"error", closeErr,
				"duration", time.Since(closeStart))
			// Continue anyway - we're shutting down or reloading
		} else {
			log.Info("application closed successfully",
				"duration", time.Since(closeStart))
		}

		// Exit or reload
		if !shouldReload {
			log.Info("shutdown complete")
			return runErr
		}

		log.Info("reloading rules and re-establishing connections")
		// Loop continues to reload
	}
}
