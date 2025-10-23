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
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           *broker.NATSBroker
	httpServer       *http.Server // This is the metrics server from the BaseApp
	metricsCollector *metrics.MetricsCollector
}

// NewRouterApp creates a new rule-router application instance using the pre-built base components.
func NewRouterApp(base *BaseApp, cfg *config.Config) (*RouterApp, error) {
	app := &RouterApp{
		config:           cfg,
		logger:           base.Logger,
		metrics:          base.Metrics,
		processor:        base.Processor,
		broker:           base.Broker,
		httpServer:       base.MetricsServer,
		metricsCollector: base.Collector,
	}

	// Initialize subscription manager with processor
	app.broker.InitializeSubscriptionManager(app.processor)

	// Setup subscriptions for all rule subjects (router-specific logic)
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

// setupSubscriptions configures JetStream consumers and subscriptions for all NATS-triggered rules.
// This is router-specific logic, moved from the old router_setup.go.
func (app *RouterApp) setupSubscriptions() error {
	subjects := app.processor.GetSubjects()
	app.logger.Info("setting up subscriptions for rule subjects", "subjectCount", len(subjects))

	if err := app.broker.ValidateSubjects(subjects); err != nil {
		return fmt.Errorf("stream validation failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, subject := range subjects {
		app.logger.Debug("setting up subscription for subject", "subject", subject)

		if err := app.broker.CreateConsumerForSubject(subject); err != nil {
			return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
		}

		if err := app.broker.AddSubscription(subject); err != nil {
			return fmt.Errorf("failed to add subscription for subject '%s': %w", subject, err)
		}

		app.logger.Info("subscription configured", "subject", subject)

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout during subscription setup")
		default:
		}
	}

	app.logger.Info("all subscriptions configured successfully", "subscriptionCount", len(subjects))
	return nil
}
