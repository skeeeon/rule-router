// file: internal/app/gateway.go

package app

import (
	"context"
	"fmt"
	"time"

	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/gateway"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Timeout constants for GatewayApp operations
const (
	// gatewayMetricsShutdownTimeout is the maximum time to wait for the metrics server to shutdown
	gatewayMetricsShutdownTimeout = 5 * time.Second

	// outboundSetupTimeout is the maximum time to wait for outbound subscriptions to be configured
	outboundSetupTimeout = 60 * time.Second
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
	base             *BaseApp // Reference to BaseApp for proper shutdown
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
		base:             base,
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

// Run starts the application and waits for shutdown signal.
// Note: Signal handling is managed by lifecycle.RunWithReload() - do not add signal handling here
// to avoid double registration which causes unpredictable behavior.
func (app *GatewayApp) Run(ctx context.Context) error {
	// Get HTTP paths and outbound subscription count
	httpPaths := app.processor.GetHTTPPaths()
	outboundSubCount := len(app.outboundClient.GetSubscriptions())

	// Log configuration summary for transparency
	allRules := app.processor.GetAllRules()
	app.logger.Info("configuration summary",
		"totalRules", len(allRules),
		"inboundHttpPaths", httpPaths,
		"outboundSubscriptions", outboundSubCount,
		"inboundWorkers", app.config.HTTP.Server.InboundWorkerCount,
		"inboundQueueSize", app.config.HTTP.Server.InboundQueueSize,
		"kvEnabled", app.config.KV.Enabled,
		"kvBuckets", app.config.KV.Buckets)

	app.logger.Info("starting http-gateway",
		"natsUrls", app.config.NATS.URLs,
		"httpAddress", app.config.HTTP.Server.Address,
		"metricsEnabled", app.config.Metrics.Enabled)

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

	// Shutdown metrics server and wait for goroutine to exit
	if app.base != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), gatewayMetricsShutdownTimeout)
		defer cancel()
		if err := app.base.ShutdownMetricsServer(shutdownCtx); err != nil {
			errors = append(errors, err)
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
		WorkerCount:    app.config.NATS.Consumers.WorkerCount,
		FetchBatchSize: app.config.NATS.Consumers.FetchBatchSize,
		FetchTimeout:   app.config.NATS.Consumers.FetchTimeout,
		MaxAckPending:  app.config.NATS.Consumers.MaxAckPending,
		AckWaitTimeout: app.config.NATS.Consumers.AckWaitTimeout,
		MaxDeliver:     app.config.NATS.Consumers.MaxDeliver,
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

	// Create a context with timeout for subscription setup
	setupCtx, cancel := context.WithTimeout(context.Background(), outboundSetupTimeout)
	defer cancel()

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

			streamName, err := app.broker.FindStreamForSubject(subject)
			if err != nil {
				return fmt.Errorf("failed to find stream for subject '%s': %w", subject, err)
			}
			consumerName := app.broker.GetConsumerName(subject)

			workers := app.config.NATS.Consumers.WorkerCount
			if err := app.outboundClient.AddSubscription(setupCtx, streamName, consumerName, subject, workers); err != nil {
				return fmt.Errorf("failed to add outbound subscription for '%s': %w", subject, err)
			}
		}
	}

	app.logger.Info("outbound client configured", "subscriptions", len(outboundSubjects))
	return nil
}


