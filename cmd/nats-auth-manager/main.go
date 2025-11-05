// file: cmd/nats-auth-manager/main.go

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"
	"rule-router/internal/authmgr"
	"rule-router/internal/authmgr/providers"
	"rule-router/internal/logger"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Parse flags
	configPath := flag.String("config", "config/auth-manager.yaml", "path to config file")
	flag.Parse()

	// Load configuration
	cfg, err := authmgr.Load(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize logger
	appLogger, err := logger.NewLogger(&cfg.Logging)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	defer appLogger.Sync()

	appLogger.Info("nats-auth-manager starting", "version", "1.0.0")

	// Connect to NATS
	natsClient, err := authmgr.NewNATSClient(&cfg.NATS, &cfg.Storage, appLogger)
	if err != nil {
		return fmt.Errorf("failed to create NATS client: %w", err)
	}
	defer natsClient.Close()

	// Create providers from config
	providerList, err := createProviders(cfg.Providers, appLogger)
	if err != nil {
		return fmt.Errorf("failed to create providers: %w", err)
	}

	// Create manager
	manager := authmgr.NewManager(natsClient, providerList, appLogger)

	// Start manager
	if err := manager.Start(); err != nil {
		return fmt.Errorf("failed to start manager: %w", err)
	}

	appLogger.Info("nats-auth-manager running",
		"providers", len(providerList),
		"kvBucket", cfg.Storage.Bucket)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	appLogger.Info("shutdown signal received, stopping...")

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := manager.Stop(); err != nil {
		appLogger.Error("error during shutdown", "error", err)
	}

	select {
	case <-shutdownCtx.Done():
		appLogger.Warn("shutdown timeout exceeded")
	default:
		appLogger.Info("shutdown complete")
	}

	return nil
}

// createProviders instantiates all configured providers
func createProviders(configs []authmgr.ProviderConfig, log *logger.Logger) ([]providers.Provider, error) {
	var providerList []providers.Provider

	for _, cfg := range configs {
		var p providers.Provider
		var err error

		switch cfg.Type {
		case "oauth2":
			refreshBefore, _ := time.ParseDuration(cfg.RefreshBefore)
			p = providers.NewOAuth2Provider(
				cfg.KVKey,
				cfg.TokenURL,
				cfg.ClientID,
				cfg.ClientSecret,
				cfg.Scopes,
				refreshBefore,
			)

		case "custom-http":
			refreshEvery, _ := time.ParseDuration(cfg.RefreshEvery)
			p = providers.NewCustomHTTPProvider(
				cfg.KVKey,
				cfg.AuthURL,
				cfg.Method,
				cfg.Headers,
				cfg.Body,
				cfg.TokenPath,
				refreshEvery,
			)

		default:
			return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
		}

		providerList = append(providerList, p)
		log.Info("provider configured",
			"id", cfg.ID,
			"type", cfg.Type,
			"kvKey", cfg.KVKey)
	}

	return providerList, nil
}
