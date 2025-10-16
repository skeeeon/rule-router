// file: internal/gateway/app.go

package gateway

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// App represents the http-gateway application with all its components
type App struct {
	config           *config.Config
	rulesPath        string
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           *broker.NATSBroker
	inboundServer    *InboundServer
	outboundClient   *OutboundClient
	metricsServer    *http.Server
	metricsCollector *metrics.MetricsCollector
}

// NewApp creates a new http-gateway application instance
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
func (app *App) Run(ctx context.Context) error {
	// Setup signal handling
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	// Get HTTP paths and outbound subscription count
	httpPaths := app.processor.GetHTTPPaths()
	outboundSubCount := len(app.outboundClient.subscriptions)

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
func (app *App) Close() error {
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

// setupLogger initializes the logger
func (app *App) setupLogger() error {
	var err error
	app.logger, err = logger.NewLogger(&app.config.Logging)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	return nil
}

// setupMetrics initializes metrics collection
func (app *App) setupMetrics() error {
	if !app.config.Metrics.Enabled {
		app.logger.Info("metrics disabled")
		return nil
	}

	reg := prometheus.NewRegistry()
	var err error
	app.metrics, err = metrics.NewMetrics(reg)
	if err != nil {
		return fmt.Errorf("failed to create metrics service: %w", err)
	}

	updateInterval, err := time.ParseDuration(app.config.Metrics.UpdateInterval)
	if err != nil {
		return fmt.Errorf("invalid metrics update interval: %w", err)
	}

	app.metricsCollector = metrics.NewMetricsCollector(app.metrics, updateInterval)
	app.metricsCollector.Start()

	if err := app.setupMetricsServer(reg); err != nil {
		return fmt.Errorf("failed to setup metrics server: %w", err)
	}

	app.logger.Info("metrics initialized successfully",
		"address", app.config.Metrics.Address,
		"path", app.config.Metrics.Path,
		"updateInterval", updateInterval)

	return nil
}

// setupMetricsServer creates the Prometheus metrics HTTP server
func (app *App) setupMetricsServer(reg *prometheus.Registry) error {
	mux := http.NewServeMux()
	mux.Handle(app.config.Metrics.Path, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
	}))

	app.metricsServer = &http.Server{
		Addr:    app.config.Metrics.Address,
		Handler: mux,
	}

	go func() {
		app.logger.Info("starting metrics server",
			"address", app.config.Metrics.Address,
			"path", app.config.Metrics.Path)
		if err := app.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			app.logger.Error("metrics server error", "error", err)
		}
	}()

	return nil
}

// setupNATSBroker initializes the NATS broker with JetStream
func (app *App) setupNATSBroker() error {
	app.logger.Info("connecting to NATS JetStream server", "urls", app.config.NATS.URLs)

	// Create NATS broker (automatically initializes connection and KV)
	var err error
	app.broker, err = broker.NewNATSBroker(app.config, app.logger, app.metrics)
	if err != nil {
		return fmt.Errorf("failed to create NATS broker: %w", err)
	}

	// Initialize local KV cache if enabled
	if app.config.KV.Enabled && app.config.KV.LocalCache.Enabled {
		app.logger.Info("initializing local KV cache",
			"buckets", app.config.KV.Buckets)

		if err := app.broker.InitializeKVCache(); err != nil {
			app.logger.Error("failed to initialize local KV cache", "error", err)
			app.logger.Info("continuing with direct NATS KV access (degraded performance)")
		} else {
			localCache := app.broker.GetLocalKVCache()
			if localCache != nil && localCache.IsEnabled() {
				stats := localCache.GetStats()
				app.logger.Info("local KV cache initialized successfully", "stats", stats)
			}
		}
	}

	if app.config.KV.Enabled {
		kvStores := app.broker.GetKVStores()
		localCache := app.broker.GetLocalKVCache()

		app.logger.Info("NATS JetStream connected with KV support",
			"kvBuckets", len(kvStores),
			"configuredBuckets", app.config.KV.Buckets,
			"localCacheEnabled", localCache != nil && localCache.IsEnabled())
	} else {
		app.logger.Info("NATS JetStream connected without KV support")
	}

	return nil
}

// setupRules loads rules and creates the processor
func (app *App) setupRules() error {
	kvBuckets := []string{}
	if app.config.KV.Enabled {
		kvBuckets = app.config.KV.Buckets
	}

	rulesLoader := rule.NewRulesLoader(app.logger, kvBuckets)
	rules, err := rulesLoader.LoadFromDirectory(app.rulesPath)
	if err != nil {
		return fmt.Errorf("failed to load rules: %w", err)
	}

	var kvContext *rule.KVContext
	if app.config.KV.Enabled && app.broker != nil {
		kvStores := app.broker.GetKVStores()
		localKVCache := app.broker.GetLocalKVCache()

		kvContext = rule.NewKVContext(kvStores, app.logger, localKVCache)

		app.logger.Info("KV context created with local cache support",
			"bucketCount", len(kvStores),
			"localCacheEnabled", localKVCache.IsEnabled())
	} else {
		app.logger.Info("KV support disabled or NATS broker not ready")
	}

	// Create signature verification config
	var sigVerification *rule.SignatureVerification
	if app.config.Security.Verification.Enabled {
		sigVerification = rule.NewSignatureVerification(
			app.config.Security.Verification.Enabled,
			app.config.Security.Verification.PublicKeyHeader,
			app.config.Security.Verification.SignatureHeader,
		)
		app.logger.Info("signature verification enabled",
			"publicKeyHeader", app.config.Security.Verification.PublicKeyHeader,
			"signatureHeader", app.config.Security.Verification.SignatureHeader)
	}

	// Create processor with correct signature
	app.processor = rule.NewProcessor(
		app.logger,
		app.metrics,
		kvContext,
		sigVerification,
	)

	// Load rules into processor
	if err := app.processor.LoadRules(rules); err != nil {
		return fmt.Errorf("failed to load rules into processor: %w", err)
	}

	natsRuleCount := len(app.processor.GetSubjects())
	httpRuleCount := len(app.processor.GetHTTPPaths())

	app.logger.Info("rules loaded successfully",
		"totalRules", len(rules),
		"natsRules", natsRuleCount,
		"httpRules", httpRuleCount)

	return nil
}

// setupInboundServer creates the HTTP inbound server (HTTP → NATS)
func (app *App) setupInboundServer() error {
	// Check if there are any HTTP-triggered rules
	httpPaths := app.processor.GetHTTPPaths()
	if len(httpPaths) == 0 {
		app.logger.Info("no HTTP inbound rules configured, skipping inbound server")
		// Create a minimal server for health checks only
		app.inboundServer = NewInboundServer(
			app.logger,
			app.metrics,
			app.processor,
			app.broker.GetJetStream(),
			app.broker.GetNATSConn(),
			&ServerConfig{
				Address:             app.config.HTTP.Server.Address,
				ReadTimeout:         app.config.HTTP.Server.ReadTimeout,
				WriteTimeout:        app.config.HTTP.Server.WriteTimeout,
				IdleTimeout:         app.config.HTTP.Server.IdleTimeout,
				MaxHeaderBytes:      app.config.HTTP.Server.MaxHeaderBytes,
				ShutdownGracePeriod: app.config.HTTP.Server.ShutdownGracePeriod,
			},
			&PublishConfig{
				Mode:           app.config.NATS.Publish.Mode,
				AckTimeout:     app.config.NATS.Publish.AckTimeout,
				MaxRetries:     app.config.NATS.Publish.MaxRetries,
				RetryBaseDelay: app.config.NATS.Publish.RetryBaseDelay,
			},
		)
		return nil
	}

	// Create inbound server
	app.inboundServer = NewInboundServer(
		app.logger,
		app.metrics,
		app.processor,
		app.broker.GetJetStream(),
		app.broker.GetNATSConn(),
		&ServerConfig{
			Address:             app.config.HTTP.Server.Address,
			ReadTimeout:         app.config.HTTP.Server.ReadTimeout,
			WriteTimeout:        app.config.HTTP.Server.WriteTimeout,
			IdleTimeout:         app.config.HTTP.Server.IdleTimeout,
			MaxHeaderBytes:      app.config.HTTP.Server.MaxHeaderBytes,
			ShutdownGracePeriod: app.config.HTTP.Server.ShutdownGracePeriod,
		},
		&PublishConfig{
			Mode:           app.config.NATS.Publish.Mode,
			AckTimeout:     app.config.NATS.Publish.AckTimeout,
			MaxRetries:     app.config.NATS.Publish.MaxRetries,
			RetryBaseDelay: app.config.NATS.Publish.RetryBaseDelay,
		},
	)

	app.logger.Info("inbound server configured",
		"address", app.config.HTTP.Server.Address,
		"paths", len(httpPaths))

	return nil
}

// setupOutboundClient creates the HTTP outbound client (NATS → HTTP)
func (app *App) setupOutboundClient() error {
	// Create outbound client
	app.outboundClient = NewOutboundClient(
		app.logger,
		app.metrics,
		app.processor,
		app.broker.GetJetStream(),
		&ConsumerConfig{
			SubscriberCount: app.config.NATS.Consumers.SubscriberCount,
			FetchBatchSize:  app.config.NATS.Consumers.FetchBatchSize,
			FetchTimeout:    app.config.NATS.Consumers.FetchTimeout,
			MaxAckPending:   app.config.NATS.Consumers.MaxAckPending,
			AckWaitTimeout:  app.config.NATS.Consumers.AckWaitTimeout,
			MaxDeliver:      app.config.NATS.Consumers.MaxDeliver,
		},
		&app.config.HTTP.Client, // CHANGED: Pass the full client config struct
	)

	// Find all rules with NATS trigger + HTTP action
	allRules := app.processor.GetAllRules()
	outboundRules := make(map[string]bool) // Track unique subjects

	for _, r := range allRules {
		if r.Trigger.NATS != nil && r.Action.HTTP != nil {
			subject := r.Trigger.NATS.Subject
			if outboundRules[subject] {
				continue // Already processed this subject
			}
			outboundRules[subject] = true

			// Use StreamResolver to find the stream (just like rule-router)
			streamResolver := app.broker.GetStreamResolver()
			streamName, err := streamResolver.FindStreamForSubject(subject)
			if err != nil {
				return fmt.Errorf("failed to find stream for subject '%s': %w", subject, err)
			}

			// Generate consumer name with identical pattern to rule-router
			consumerName := app.broker.GetConsumerName(subject)

			// Create consumer using broker's pattern
			if err := app.createConsumerForOutbound(streamName, consumerName, subject); err != nil {
				return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
			}

			// Add subscription to outbound client
			workers := app.config.NATS.Consumers.SubscriberCount
			if err := app.outboundClient.AddSubscription(streamName, consumerName, subject, workers); err != nil {
				return fmt.Errorf("failed to add outbound subscription for '%s': %w", subject, err)
			}

			app.logger.Info("outbound subscription configured",
				"subject", subject,
				"stream", streamName,
				"consumer", consumerName,
				"workers", workers)
		}
	}

	app.logger.Info("outbound client configured", "subscriptions", len(outboundRules))
	return nil
}

// createConsumerForOutbound creates a JetStream consumer for outbound HTTP
func (app *App) createConsumerForOutbound(streamName, consumerName, subject string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := app.broker.GetJetStream().Stream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("failed to get stream: %w", err)
	}

	// Check if consumer already exists
	_, err = stream.Consumer(ctx, consumerName)
	if err == nil {
		app.logger.Debug("consumer already exists", "consumer", consumerName, "stream", streamName)
		return nil
	}

	// Create consumer config matching broker's pattern
	consumerConfig := jetstream.ConsumerConfig{
		Durable:       consumerName,
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       app.config.NATS.Consumers.AckWaitTimeout,
		MaxDeliver:    app.config.NATS.Consumers.MaxDeliver,
		MaxAckPending: app.config.NATS.Consumers.MaxAckPending,
		DeliverPolicy: jetstream.DeliverAllPolicy, // Match default from config
		ReplayPolicy:  jetstream.ReplayInstantPolicy,
	}

	// Create or update consumer
	_, err = stream.CreateOrUpdateConsumer(ctx, consumerConfig)
	if err != nil {
		return fmt.Errorf("failed to create consumer: %w", err)
	}

	app.logger.Info("consumer created", "consumer", consumerName, "stream", streamName, "subject", subject)
	return nil
}

// sanitizeSubject converts a NATS subject to a valid consumer name (identical to rule-router)
func sanitizeSubject(subject string) string {
	s := strings.ReplaceAll(subject, ".", "-")
	s = strings.ReplaceAll(s, "*", "wildcard")
	s = strings.ReplaceAll(s, ">", "multi")
	return s
}
