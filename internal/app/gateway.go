// file: internal/app/gateway.go

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
	"rule-router/internal/gateway"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Verify GatewayApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*GatewayApp)(nil)

// GatewayApp represents the http-gateway application with all its components
type GatewayApp struct {
	config           *config.Config
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           *broker.NATSBroker
	inboundServer    *gateway.InboundServer
	outboundClient   *gateway.OutboundClient
	metricsServer    *http.Server
	metricsCollector *metrics.MetricsCollector
}

// NewGatewayApp creates a new http-gateway application instance using the pre-built base components.
func NewGatewayApp(base *BaseApp, cfg *config.Config) (*GatewayApp, error) {
	app := &GatewayApp{
		config:           cfg,
		logger:           base.Logger,
		metrics:          base.Metrics,
		processor:        base.Processor,
		broker:           base.Broker,
		metricsServer:    base.MetricsServer,
		metricsCollector: base.Collector,
	}

	// Validate gateway-specific configuration requirements
	if cfg.HTTP.Server.Address == "" {
		return nil, fmt.Errorf("HTTP server address is required for http-gateway")
	}

	// Setup inbound server (HTTP → NATS)
	if err := app.setupInboundServer(); err != nil {
		return nil, fmt.Errorf("failed to setup inbound server: %w", err)
	}

	// Setup outbound client (NATS → HTTP)
	if err := app.setupOutboundClient(); err != nil {
		return nil, fmt.Errorf("failed to setup outbound client: %w", err)
	}

	return app, nil
}

// Run starts the application and waits for shutdown signal
func (app *GatewayApp) Run(ctx context.Context) error {
	// Setup signal handling (lifecycle package will handle SIGHUP)
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	// Get HTTP paths and outbound subscription count
	httpPaths := app.processor.GetHTTPPaths()
	outboundSubCount := len(app.outboundClient.GetSubscriptions())

	app.logger.Info("starting http-gateway",
		"natsUrls", app.config.NATS.URLs,
		"httpAddress", app.config.HTTP.Server.Address,
		"httpPaths", len(httpPaths),
		"outboundSubscriptions", outboundSubCount,
		"metricsEnabled", app.config.Metrics.Enabled,
		"kvEnabled", app.config.KV.Enabled)

	// Start inbound HTTP server
	if err := app.inboundServer.Start(ctx); err != nil {
		return fmt.Errorf("failed to start inbound server: %w", err)
	}

	// Start outbound client
	if err := app.outboundClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start outbound client: %w", err)
	}

	app.logger.Info("http-gateway started successfully",
		"inboundPaths", httpPaths,
		"outboundSubjects", outboundSubCount)

	// Update metrics
	if app.metrics != nil {
		allRules := app.processor.GetAllRules()
		app.metrics.SetRulesActive(float64(len(allRules)))
	}

	// Wait for shutdown signal
	<-ctx.Done()
	app.logger.Info("shutting down gracefully...")

	// Create shutdown context with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), app.config.HTTP.Server.ShutdownGracePeriod)
	defer shutdownCancel()

	// Stop inbound server
	if err := app.inboundServer.Stop(shutdownCtx); err != nil {
		app.logger.Error("failed to stop inbound server", "error", err)
	}

	// Stop outbound client
	if err := app.outboundClient.Stop(); err != nil {
		app.logger.Error("failed to stop outbound client", "error", err)
	}

	app.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down all application components
func (app *GatewayApp) Close() error {
	app.logger.Info("closing application components")

	var errors []error

	// Stop metrics collector
	if app.metricsCollector != nil {
		app.metricsCollector.Stop()
	}

	// Shutdown metrics server
	if app.metricsServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.metricsServer.Shutdown(shutdownCtx); err != nil {
			errors = append(errors, fmt.Errorf("failed to shutdown metrics server: %w", err))
		}
	}

	// Close NATS broker
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

// setupInboundServer configures the server for handling HTTP -> NATS traffic.
// This is gateway-specific logic.
func (app *GatewayApp) setupInboundServer() error {
	serverConfig := &gateway.ServerConfig{
		Address:             app.config.HTTP.Server.Address,
		ReadTimeout:         app.config.HTTP.Server.ReadTimeout,
		WriteTimeout:        app.config.HTTP.Server.WriteTimeout,
		IdleTimeout:         app.config.HTTP.Server.IdleTimeout,
		MaxHeaderBytes:      app.config.HTTP.Server.MaxHeaderBytes,
		ShutdownGracePeriod: app.config.HTTP.Server.ShutdownGracePeriod,
		InboundWorkerCount:  app.config.HTTP.Server.InboundWorkerCount,
		InboundQueueSize:    app.config.HTTP.Server.InboundQueueSize,
	}
	publishConfig := &gateway.PublishConfig{
		Mode:           app.config.NATS.Publish.Mode,
		AckTimeout:     app.config.NATS.Publish.AckTimeout,
		MaxRetries:     app.config.NATS.Publish.MaxRetries,
		RetryBaseDelay: app.config.NATS.Publish.RetryBaseDelay,
	}

	app.inboundServer = gateway.NewInboundServer(
		app.logger,
		app.metrics,
		app.processor,
		app.broker.GetJetStream(),
		app.broker.GetNATSConn(),
		serverConfig,
		publishConfig,
	)

	app.logger.Info("inbound server configured", "address", serverConfig.Address)
	return nil
}

// setupOutboundClient configures the client for handling NATS -> HTTP traffic.
// This is gateway-specific logic.
func (app *GatewayApp) setupOutboundClient() error {
	consumerConfig := &gateway.ConsumerConfig{
		SubscriberCount: app.config.NATS.Consumers.SubscriberCount,
		FetchBatchSize:  app.config.NATS.Consumers.FetchBatchSize,
		FetchTimeout:    app.config.NATS.Consumers.FetchTimeout,
		MaxAckPending:   app.config.NATS.Consumers.MaxAckPending,
		AckWaitTimeout:  app.config.NATS.Consumers.AckWaitTimeout,
		MaxDeliver:      app.config.NATS.Consumers.MaxDeliver,
	}

	app.outboundClient = gateway.NewOutboundClient(
		app.logger,
		app.metrics,
		app.processor,
		app.broker.GetJetStream(),
		consumerConfig,
		&app.config.HTTP.Client,
	)

	// Find all rules with NATS trigger + HTTP action to create subscriptions
	allRules := app.processor.GetAllRules()
	outboundSubjects := make(map[string]bool)

	for _, r := range allRules {
		if r.Trigger.NATS != nil && r.Action.HTTP != nil {
			subject := r.Trigger.NATS.Subject
			if outboundSubjects[subject] {
				continue // Already processed
			}
			outboundSubjects[subject] = true

			if err := app.broker.CreateConsumerForSubject(subject); err != nil {
				return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
			}

			streamName, err := app.broker.GetStreamResolver().FindStreamForSubject(subject)
			if err != nil {
				return fmt.Errorf("failed to find stream for subject '%s': %w", subject, err)
			}
			consumerName := app.broker.GetConsumerName(subject)

			workers := app.config.NATS.Consumers.SubscriberCount
			if err := app.outboundClient.AddSubscription(streamName, consumerName, subject, workers); err != nil {
				return fmt.Errorf("failed to add outbound subscription for '%s': %w", subject, err)
			}
		}
	}

	app.logger.Info("outbound client configured", "subscriptions", len(outboundSubjects))
	return nil
}
