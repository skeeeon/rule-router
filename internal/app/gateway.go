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
	rulesPath        string
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           *broker.NATSBroker
	inboundServer    *gateway.InboundServer
	outboundClient   *gateway.OutboundClient
	metricsServer    *http.Server
	metricsCollector *metrics.MetricsCollector
}

// NewGatewayApp creates a new http-gateway application instance
func NewGatewayApp(cfg *config.Config, rulesPath string) (*GatewayApp, error) {
	app := &GatewayApp{
		config:    cfg,
		rulesPath: rulesPath,
	}

	// Validate gateway-specific configuration requirements
	if cfg.HTTP.Server.Address == "" {
		return nil, fmt.Errorf("HTTP server address is required for http-gateway")
	}

	// Initialize components in dependency order
	if err := app.setupLogger(); err != nil {
		return nil, fmt.Errorf("failed to setup logger: %w", err)
	}

	if err := app.setupMetrics(); err != nil {
		return nil, fmt.Errorf("failed to setup metrics: %w", err)
	}

	// Setup NATS broker first (needed for KV stores and stream resolution)
	if err := app.setupNATSBroker(); err != nil {
		return nil, fmt.Errorf("failed to setup NATS broker: %w", err)
	}

	// Setup rules after broker (so KV stores are available)
	if err := app.setupRules(); err != nil {
		return nil, fmt.Errorf("failed to setup rules: %w", err)
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
