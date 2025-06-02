//file: internal/app/app.go

package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"rule-router/config"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// BrokerInterface defines the interface for message brokers
type BrokerInterface interface {
	GetPublisher() message.Publisher
	GetSubscriber() message.Subscriber
	GetRouter() *message.Router
	Close() error
}

// App represents the main application with all its components
type App struct {
	config           *config.Config
	rulesPath        string
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           BrokerInterface
	router           *message.Router
	httpServer       *http.Server
	metricsCollector *metrics.MetricsCollector
}

// NewApp creates a new application instance with all components initialized
func NewApp(cfg *config.Config, rulesPath string) (*App, error) {
	app := &App{
		config:    cfg,
		rulesPath: rulesPath,
	}

	// Initialize components in dependency order
	if err := app.setupLogger(); err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}

	if err := app.setupMetrics(); err != nil {
		return nil, fmt.Errorf("failed to setup metrics: %w", err)
	}

	if err := app.setupRules(); err != nil {
		return nil, fmt.Errorf("failed to setup rules: %w", err)
	}

	if err := app.setupBroker(); err != nil {
		return nil, fmt.Errorf("failed to setup broker: %w", err)
	}

	if err := app.setupRouter(); err != nil {
		return nil, fmt.Errorf("failed to setup router: %w", err)
	}

	return app, nil
}

// Run starts the application and waits for shutdown signal
func (a *App) Run(ctx context.Context) error {
	// Setup signal handling
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	// Start the router
	a.logger.Info("starting Watermill router with external broker connections",
		"brokerType", a.config.BrokerType,
		"externalBroker", a.getExternalBrokerInfo(),
		"workers", a.config.Processing.Workers,
		"topicsCount", len(a.processor.GetTopics()),
		"metricsEnabled", a.config.Metrics.Enabled)

	go func() {
		if err := a.router.Run(ctx); err != nil {
			a.logger.Error("router stopped with error", "error", err)
		}
	}()

	// Wait for router to be ready
	<-a.router.Running()
	a.logger.Info("Watermill router is running and ready to process messages")

	// Update metrics
	if a.metrics != nil {
		topics := a.processor.GetTopics()
		a.metrics.SetRulesActive(float64(len(topics)))
		a.metrics.SetWorkerPoolActive(float64(a.config.Processing.Workers))
	}

	// Wait for shutdown signal
	<-ctx.Done()
	a.logger.Info("shutting down gracefully...")

	// Close the router
	if err := a.router.Close(); err != nil {
		a.logger.Error("failed to close router", "error", err)
		return err
	}

	a.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down all application components
func (a *App) Close() error {
	a.logger.Info("closing application components")

	var errors []error

	// Stop metrics collector
	if a.metricsCollector != nil {
		a.metricsCollector.Stop()
	}

	// Shutdown HTTP server
	if a.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			errors = append(errors, fmt.Errorf("failed to shutdown metrics server: %w", err))
		}
	}

	// Close router (if not already closed in Run())
	if a.router != nil {
		if err := a.router.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close router: %w", err))
		}
	}

	// Close broker
	if a.broker != nil {
		if err := a.broker.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close broker: %w", err))
		}
	}

	// Close processor
	if a.processor != nil {
		a.processor.Close()
	}

	// Sync logger
	if a.logger != nil {
		if err := a.logger.Sync(); err != nil {
			// Don't add sync errors to the error list as they're often benign
			a.logger.Debug("logger sync completed", "error", err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %v", errors)
	}

	return nil
}

// getExternalBrokerInfo returns broker connection info for logging
func (a *App) getExternalBrokerInfo() string {
	switch a.config.BrokerType {
	case "nats":
		if len(a.config.NATS.URLs) > 0 {
			return a.config.NATS.URLs[0]
		}
		return "nats://localhost:4222"
	case "mqtt":
		return a.config.MQTT.Broker
	default:
		return "unknown"
	}
}
