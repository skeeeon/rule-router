// file: internal/app/gateway_setup.go

package app

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"rule-router/internal/broker"
	"rule-router/internal/gateway"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

func (app *GatewayApp) setupLogger() error {
	var err error
	app.logger, err = logger.NewLogger(&app.config.Logging)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	return nil
}

func (app *GatewayApp) setupMetrics() error {
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

func (app *GatewayApp) setupMetricsServer(reg *prometheus.Registry) error {
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

func (app *GatewayApp) setupNATSBroker() error {
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

func (app *GatewayApp) setupRules() error {
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

	// Create processor
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

func (app *GatewayApp) setupInboundServer() error {
	// Check if there are any HTTP-triggered rules
	httpPaths := app.processor.GetHTTPPaths()
	if len(httpPaths) == 0 {
		app.logger.Info("no HTTP inbound rules configured, skipping inbound server")
		// Create a minimal server for health checks only
		app.inboundServer = gateway.NewInboundServer(
			app.logger,
			app.metrics,
			app.processor,
			app.broker.GetJetStream(),
			app.broker.GetNATSConn(),
			&gateway.ServerConfig{
				Address:             app.config.HTTP.Server.Address,
				ReadTimeout:         app.config.HTTP.Server.ReadTimeout,
				WriteTimeout:        app.config.HTTP.Server.WriteTimeout,
				IdleTimeout:         app.config.HTTP.Server.IdleTimeout,
				MaxHeaderBytes:      app.config.HTTP.Server.MaxHeaderBytes,
				ShutdownGracePeriod: app.config.HTTP.Server.ShutdownGracePeriod,
			},
			&gateway.PublishConfig{
				Mode:           app.config.NATS.Publish.Mode,
				AckTimeout:     app.config.NATS.Publish.AckTimeout,
				MaxRetries:     app.config.NATS.Publish.MaxRetries,
				RetryBaseDelay: app.config.NATS.Publish.RetryBaseDelay,
			},
		)
		return nil
	}

	// Create inbound server
	app.inboundServer = gateway.NewInboundServer(
		app.logger,
		app.metrics,
		app.processor,
		app.broker.GetJetStream(),
		app.broker.GetNATSConn(),
		&gateway.ServerConfig{
			Address:             app.config.HTTP.Server.Address,
			ReadTimeout:         app.config.HTTP.Server.ReadTimeout,
			WriteTimeout:        app.config.HTTP.Server.WriteTimeout,
			IdleTimeout:         app.config.HTTP.Server.IdleTimeout,
			MaxHeaderBytes:      app.config.HTTP.Server.MaxHeaderBytes,
			ShutdownGracePeriod: app.config.HTTP.Server.ShutdownGracePeriod,
		},
		&gateway.PublishConfig{
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

func (app *GatewayApp) setupOutboundClient() error {
	// Create outbound client
	app.outboundClient = gateway.NewOutboundClient(
		app.logger,
		app.metrics,
		app.processor,
		app.broker.GetJetStream(),
		&gateway.ConsumerConfig{
			SubscriberCount: app.config.NATS.Consumers.SubscriberCount,
			FetchBatchSize:  app.config.NATS.Consumers.FetchBatchSize,
			FetchTimeout:    app.config.NATS.Consumers.FetchTimeout,
			MaxAckPending:   app.config.NATS.Consumers.MaxAckPending,
			AckWaitTimeout:  app.config.NATS.Consumers.AckWaitTimeout,
			MaxDeliver:      app.config.NATS.Consumers.MaxDeliver,
		},
		&app.config.HTTP.Client,
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

			// Delegate consumer creation to the broker
			if err := app.broker.CreateConsumerForSubject(subject); err != nil {
				return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
			}

			// Get stream and consumer names from broker
			streamResolver := app.broker.GetStreamResolver()
			streamName, err := streamResolver.FindStreamForSubject(subject)
			if err != nil {
				return fmt.Errorf("failed to find stream for subject '%s': %w", subject, err)
			}
			consumerName := app.broker.GetConsumerName(subject)

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
