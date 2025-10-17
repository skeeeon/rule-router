// file: internal/app/router.go

package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Verify RouterApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*RouterApp)(nil)

// RouterApp represents the rule-router application with all its components including KV support
type RouterApp struct {
	config           *config.Config
	rulesPath        string
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           *broker.NATSBroker
	httpServer       *http.Server
	metricsCollector *metrics.MetricsCollector
}

// NewRouterApp creates a new rule-router application instance with all components initialized
func NewRouterApp(cfg *config.Config, rulesPath string) (*RouterApp, error) {
	app := &RouterApp{
		config:    cfg,
		rulesPath: rulesPath,
	}

	// Validate router-specific configuration requirements
	if len(cfg.NATS.URLs) == 0 {
		return nil, fmt.Errorf("at least one NATS URL is required")
	}

	// Initialize components in dependency order
	if err := app.setupLogger(); err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}

	if err := app.setupMetrics(); err != nil {
		return nil, fmt.Errorf("failed to setup metrics: %w", err)
	}

	// Setup NATS broker first (needed for KV stores)
	if err := app.setupNATSBroker(); err != nil {
		return nil, fmt.Errorf("failed to setup NATS broker: %w", err)
	}

	// Setup rules after broker (so KV stores are available)
	if err := app.setupRules(); err != nil {
		return nil, fmt.Errorf("failed to setup rules: %w", err)
	}

	// Initialize subscription manager with processor
	app.broker.InitializeSubscriptionManager(app.processor)

	// Setup subscriptions for all rule subjects
	if err := app.setupSubscriptions(); err != nil {
		return nil, fmt.Errorf("failed to setup subscriptions: %w", err)
	}

	return app, nil
}

// Run starts the application and waits for shutdown signal
func (app *RouterApp) Run(ctx context.Context) error {
	// Setup signal handling (lifecycle package will handle SIGHUP)
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	// Get subscription count
	subMgr := app.broker.GetSubscriptionManager()
	subCount := subMgr.GetSubscriptionCount()

	app.logger.Info("starting rule-router with NATS JetStream",
		"natsUrls", app.config.NATS.URLs,
		"subscriptionCount", subCount,
		"metricsEnabled", app.config.Metrics.Enabled,
		"kvEnabled", app.config.KV.Enabled,
		"kvBuckets", app.config.KV.Buckets)

	// Start subscription manager
	if err := subMgr.Start(ctx); err != nil {
		return fmt.Errorf("failed to start subscription manager: %w", err)
	}

	app.logger.Info("all subscriptions active and processing messages")

	// Update metrics
	if app.metrics != nil {
		subjects := app.processor.GetSubjects()
		app.metrics.SetRulesActive(float64(len(subjects)))
	}

	// Wait for shutdown signal
	<-ctx.Done()
	app.logger.Info("shutting down gracefully...")

	// Stop subscription manager
	if err := subMgr.Stop(); err != nil {
		app.logger.Error("failed to stop subscription manager", "error", err)
		return err
	}

	app.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down all application components
func (app *RouterApp) Close() error {
	app.logger.Info("closing application components")

	var errors []error

	// Stop metrics collector
	if app.metricsCollector != nil {
		app.metricsCollector.Stop()
	}

	// Shutdown HTTP server
	if app.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.httpServer.Shutdown(shutdownCtx); err != nil {
			errors = append(errors, fmt.Errorf("failed to shutdown metrics server: %w", err))
		}
	}

	// Close NATS broker (this will stop subscriptions and clean up)
	if app.broker != nil {
		if err := app.broker.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close NATS broker: %w", err))
		}
	}

	// NOTE: Processor doesn't need cleanup - it's stateless with no connections or goroutines

	// Sync logger
	if app.logger != nil {
		if err := app.logger.Sync(); err != nil {
			app.logger.Debug("logger sync completed", "error", err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %v", errors)
	}

	return nil
}
