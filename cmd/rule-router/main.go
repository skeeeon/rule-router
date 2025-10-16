// file: cmd/rule-router/main.go

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rule-router/config"
	"rule-router/internal/app"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Parse flags once - reuse on reload
	cfg, rulesPath := parseFlags()

	reloadCount := 0
	for {
		if reloadCount > 0 {
			log.Printf("♻️  Reloading rule-router (reload #%d)\n", reloadCount)
		}

		// Create signal channels
		shutdownSig := make(chan os.Signal, 1)
		reloadSig := make(chan os.Signal, 1)

		signal.Notify(shutdownSig, os.Interrupt, syscall.SIGTERM)
		signal.Notify(reloadSig, syscall.SIGHUP)

		// Create app
		startTime := time.Now()
		application, err := app.NewApp(cfg, rulesPath)
		if err != nil {
			if reloadCount > 0 {
				log.Printf("❌ FATAL: Failed to reload after %d successful reloads: %v\n", reloadCount, err)
				log.Println("💡 Fix the configuration/rules and restart the process")
			}
			return fmt.Errorf("failed to create app: %w", err)
		}

		// Run in goroutine
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- application.Run(ctx)
		}()

		// Wait for signal or error
		var shouldReload bool
		var runErr error

		select {
		case sig := <-shutdownSig:
			log.Printf("🛑 Shutdown signal received: %v\n", sig)
			shouldReload = false

		case <-reloadSig:
			reloadDuration := time.Since(startTime)
			log.Printf("🔄 SIGHUP received - initiating graceful reload (uptime: %v)\n", reloadDuration)
			shouldReload = true
			reloadCount++

		case runErr = <-errCh:
			log.Printf("❌ Application error: %v\n", runErr)
			shouldReload = false
		}

		// Cleanup
		log.Println("⏳ Shutting down gracefully...")
		shutdownStart := time.Now()

		cancel() // Signal app.Run() to stop

		// Wait for Run() to complete (with timeout)
		select {
		case <-errCh:
			// Run() completed
		case <-time.After(30 * time.Second):
			log.Println("⚠️  Timeout waiting for Run() to complete, forcing shutdown")
		}

		// Close application (drains connections)
		if err := application.Close(); err != nil {
			log.Printf("⚠️  Error during shutdown: %v\n", err)
		}

		// Cleanup signal handlers
		signal.Stop(shutdownSig)
		signal.Stop(reloadSig)
		close(shutdownSig)
		close(reloadSig)

		shutdownDuration := time.Since(shutdownStart)
		log.Printf("✅ Shutdown complete (took %v)\n", shutdownDuration)

		if !shouldReload {
			return runErr
		}

		log.Printf("🔄 Reloading from rules directory: %s\n", rulesPath)
		// Continue to reload
	}
}

// parseFlags parses command line arguments and applies overrides
func parseFlags() (*config.Config, string) {
	configPath := flag.String("config", "config/config.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")

	// Metrics overrides
	metricsAddrOverride := flag.String("metrics-addr", "", "override metrics server address (empty = use config)")
	metricsPathOverride := flag.String("metrics-path", "", "override metrics endpoint path (empty = use config)")
	metricsIntervalOverride := flag.Duration("metrics-interval", 0, "override metrics collection interval (0 = use config)")

	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Apply command line overrides (metrics only)
	cfg.ApplyOverrides(
		*metricsAddrOverride,
		*metricsPathOverride,
		*metricsIntervalOverride,
	)

	return cfg, *rulesPath
}
