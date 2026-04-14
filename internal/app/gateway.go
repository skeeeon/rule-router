// file: internal/app/gateway.go

package app

import (
	"context"
	"fmt"
	"time"

	"rule-router/config"
	"rule-router/internal/gateway"
	"rule-router/internal/httpclient"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Timeout constants for GatewayApp operations
const (
	// outboundSetupTimeout is the maximum time to wait for outbound subscriptions to be configured
	outboundSetupTimeout = 60 * time.Second
)

// Verify GatewayApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*GatewayApp)(nil)

// GatewayApp represents the http-gateway application with all its components.
// Outbound NATS-trigger+HTTP-action rules are handled by the shared SubscriptionManager
// with an injected HTTP executor — no separate OutboundClient needed.
type GatewayApp struct {
	config        *config.Config
	logger        *logger.Logger
	metrics       *metrics.Metrics
	processor     *rule.Processor
	inboundServer *gateway.InboundServer
	base          *BaseApp
}

// NewGatewayApp creates a new http-gateway application instance using the pre-built base components.
// In KV mode, the caller must have already started base.RuleKVManager via the builder.
func NewGatewayApp(base *BaseApp, cfg *config.Config) (*GatewayApp, error) {
	app := &GatewayApp{
		config:    cfg,
		logger:    base.Logger.With("component", "gateway-app"),
		metrics:   base.Metrics,
		processor: base.Processor,
		base:      base,
	}

	// Wire HTTP executor into the shared SubscriptionManager so it can handle
	// NATS-trigger + HTTP-action rules alongside NATS-trigger + NATS-action rules.
	httpExec := httpclient.NewHTTPExecutor(&cfg.HTTP.Client, base.Logger, base.Metrics)
	base.Broker.GetSubscriptionManager().SetHTTPExecutor(httpExec)

	// Setup inbound server (HTTP → NATS)
	if err := app.setupInboundServer(); err != nil {
		return nil, fmt.Errorf("failed to setup inbound server: %w", err)
	}

	if cfg.KV.Rules.Enabled {
		// KV rules mode: inbound uses catch-all handler, outbound managed by KV manager
		app.inboundServer.SetKVMode(true)
		// KV manager handles outbound subscriptions via broker.AddAndStartSubscription
	} else {
		// File-based mode: setup outbound subscriptions for NATS-trigger + HTTP-action rules
		if err := app.setupOutboundSubscriptions(); err != nil {
			return nil, fmt.Errorf("failed to setup outbound subscriptions: %w", err)
		}
	}

	return app, nil
}

// Run starts the application and waits for shutdown signal.
func (app *GatewayApp) Run(ctx context.Context) error {
	httpPaths := app.processor.GetHTTPPaths()

	// Log configuration summary
	allRules := app.processor.GetAllRules()
	app.logger.Info("configuration summary",
		"totalRules", len(allRules),
		"inboundHttpPaths", httpPaths,
		"inboundWorkers", app.config.HTTP.Server.InboundWorkerCount,
		"inboundQueueSize", app.config.HTTP.Server.InboundQueueSize,
		"kvEnabled", app.config.KV.Enabled,
		"kvBuckets", app.config.KV.BucketNames())

	app.logger.Info("starting http-gateway",
		"urls", app.config.NATS.URLs,
		"httpAddress", app.config.HTTP.Server.Address,
		"metricsEnabled", app.config.Metrics.Enabled)

	// Start inbound HTTP server
	if err := app.inboundServer.Start(ctx); err != nil {
		return fmt.Errorf("failed to start inbound server: %w", err)
	}

	app.logger.Info("http-gateway started successfully",
		"inboundPaths", httpPaths)

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

	app.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down gateway-specific resources.
// Shared resources (metrics, broker, logger) are cleaned up by BaseApp.Close().
func (app *GatewayApp) Close() error {
	app.logger.Info("closing gateway components")
	return nil
}

// setupInboundServer configures the server for handling HTTP -> NATS traffic.
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
		app.base.Broker.GetJetStream(),
		app.base.Broker.GetNATSConn(),
		serverConfig,
		publishConfig,
	)

	app.logger.Info("inbound server configured", "address", serverConfig.Address)
	return nil
}

// setupOutboundSubscriptions creates subscriptions for NATS-trigger + HTTP-action rules
// on the shared SubscriptionManager. Same pattern as router's setupSubscriptions.
func (app *GatewayApp) setupOutboundSubscriptions() error {
	allRules := app.processor.GetAllRules()
	outboundSubjects := make(map[string]bool)

	for _, r := range allRules {
		if r.Trigger.NATS != nil && r.Action.HTTP != nil {
			subject := r.Trigger.NATS.Subject
			if outboundSubjects[subject] {
				continue
			}
			outboundSubjects[subject] = true

			if err := app.base.Broker.CreateConsumerForSubject(subject); err != nil {
				return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
			}

			if err := app.base.Broker.AddSubscription(subject); err != nil {
				return fmt.Errorf("failed to add subscription for subject '%s': %w", subject, err)
			}
		}
	}

	app.logger.Info("outbound subscriptions configured", "count", len(outboundSubjects))
	return nil
}
