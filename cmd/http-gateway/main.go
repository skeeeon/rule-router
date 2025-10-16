// file: cmd/http-gateway/main.go

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
	"rule-router/internal/gateway"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Parse flags once - reuse on reload
	configPath := flag.String("config", "config/http-gateway.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")
	flag.Parse()

	// Load configuration once
	cfg, err := config.LoadHTTPConfig(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	reloadCount := 0
	for {
		if reloadCount > 0 {
			log.Printf("♻️  Reloading http-gateway (reload #%d)\n", reloadCount)
			log.Println("⚠️  HTTP endpoints will be briefly unavailable during reload")
		}

		// Create signal channels
		shutdownSig := make(chan os.Signal, 1)
		reloadSig := make(chan os.Signal, 1)

		signal.Notify(shutdownSig, os.Interrupt, syscall.SIGTERM)
		signal.Notify(reloadSig, syscall.SIGHUP)

		// Create app
		startTime := time.Now()
		app, err := gateway.NewApp(cfg, *rulesPath)
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
			errCh <- app.Run(ctx)
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
			log.Println("📡 HTTP server will stop accepting requests during reload")
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

		// Close application (drains connections, stops HTTP server)
		if err := app.Close(); err != nil {
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

		log.Printf("🔄 Reloading from rules directory: %s\n", *rulesPath)
		log.Println("🔌 Webhook senders may receive connection errors during reload (~100-500ms)")
		// Continue to reload
	}
}
