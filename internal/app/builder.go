// file: internal/app/builder.go

package app

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// BaseApp holds the common, initialized components for any application.
type BaseApp struct {
	Logger        *logger.Logger
	Metrics       *metrics.Metrics
	Broker        *broker.NATSBroker
	Processor     *rule.Processor
	MetricsServer *http.Server
	Collector     *metrics.MetricsCollector

	// metricsServerWg tracks the metrics server goroutine for graceful shutdown
	metricsServerWg sync.WaitGroup
}

// AppBuilder constructs the BaseApp components fluently.
type AppBuilder struct {
	cfg       *config.Config
	rulesPath string
	base      *BaseApp
	err       error
}

// NewAppBuilder creates a new builder.
func NewAppBuilder(cfg *config.Config, rulesPath string) *AppBuilder {
	return &AppBuilder{
		cfg:       cfg,
		rulesPath: rulesPath,
		base:      &BaseApp{},
	}
}

// WithLogger creates the logger.
func (b *AppBuilder) WithLogger() *AppBuilder {
	if b.err != nil {
		return b
	}
	b.base.Logger, b.err = logger.NewLogger(&b.cfg.Logging)
	if b.err != nil {
		b.err = fmt.Errorf("failed to initialize logger: %w", b.err)
	}
	return b
}

// WithMetrics creates the metrics components.
func (b *AppBuilder) WithMetrics() *AppBuilder {
	if b.err != nil {
		return b
	}
	if !b.cfg.Metrics.Enabled {
		b.base.Logger.Info("metrics disabled")
		return b
	}

	reg := prometheus.NewRegistry()
	var err error
	b.base.Metrics, err = metrics.NewMetrics(reg)
	if err != nil {
		b.err = fmt.Errorf("failed to create metrics service: %w", err)
		return b
	}

	updateInterval, err := time.ParseDuration(b.cfg.Metrics.UpdateInterval)
	if err != nil {
		b.err = fmt.Errorf("invalid metrics update interval: %w", err)
		return b
	}

	b.base.Collector = metrics.NewMetricsCollector(b.base.Metrics, updateInterval)
	b.base.Collector.Start()

	// Setup and start the metrics server
	mux := http.NewServeMux()
	mux.Handle(b.cfg.Metrics.Path, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
	}))

	b.base.MetricsServer = &http.Server{
		Addr:    b.cfg.Metrics.Address,
		Handler: mux,
	}

	// Track the metrics server goroutine with WaitGroup for graceful shutdown
	b.base.metricsServerWg.Add(1)
	go func() {
		defer b.base.metricsServerWg.Done()
		b.base.Logger.Info("starting metrics server",
			"address", b.cfg.Metrics.Address,
			"path", b.cfg.Metrics.Path)
		if err := b.base.MetricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			b.base.Logger.Error("metrics server error", "error", err)
		}
	}()

	b.base.Logger.Info("metrics initialized successfully",
		"address", b.cfg.Metrics.Address,
		"path", b.cfg.Metrics.Path,
		"updateInterval", updateInterval)

	return b
}

// WithNATSBroker creates the NATS broker and initializes the KV cache.
func (b *AppBuilder) WithNATSBroker() *AppBuilder {
	if b.err != nil {
		return b
	}
	b.base.Logger.Info("connecting to NATS JetStream server", "urls", b.cfg.NATS.URLs)

	b.base.Broker, b.err = broker.NewNATSBroker(b.cfg, b.base.Logger, b.base.Metrics)
	if b.err != nil {
		b.err = fmt.Errorf("failed to create NATS broker: %w", b.err)
		return b
	}

	// Initialize local KV cache if enabled
	if b.cfg.KV.Enabled && b.cfg.KV.LocalCache.Enabled {
		b.base.Logger.Info("initializing local KV cache", "buckets", b.cfg.KV.Buckets)
		if err := b.base.Broker.InitializeKVCache(); err != nil {
			b.base.Logger.Error("failed to initialize local KV cache, continuing with direct NATS KV access", "error", err)
		} else {
			b.base.Logger.Info("local KV cache initialized successfully")
		}
	}
	return b
}

// WithRuleProcessor loads rules and creates the rule processor.
func (b *AppBuilder) WithRuleProcessor() *AppBuilder {
	if b.err != nil {
		return b
	}

	kvBuckets := []string{}
	if b.cfg.KV.Enabled {
		kvBuckets = b.cfg.KV.Buckets
	}

	rulesLoader := rule.NewRulesLoader(b.base.Logger, kvBuckets)
	rules, err := rulesLoader.LoadFromDirectory(b.rulesPath)
	if err != nil {
		b.err = fmt.Errorf("failed to load rules: %w", err)
		return b
	}

	var kvContext *rule.KVContext
	if b.cfg.KV.Enabled && b.base.Broker != nil {
		kvStores := b.base.Broker.GetKVStores()
		localKVCache := b.base.Broker.GetLocalKVCache()
		kvContext = rule.NewKVContext(kvStores, b.base.Logger, localKVCache)
	}

	var sigVerification *rule.SignatureVerification
	if b.cfg.Security.Verification.Enabled {
		sigVerification = rule.NewSignatureVerification(
			b.cfg.Security.Verification.Enabled,
			b.cfg.Security.Verification.PublicKeyHeader,
			b.cfg.Security.Verification.SignatureHeader,
		)
	}

	b.base.Processor = rule.NewProcessor(b.base.Logger, b.base.Metrics, kvContext, sigVerification)
	
	// Configure forEach iteration limit
	b.base.Processor.SetMaxForEachIterations(b.cfg.ForEach.MaxIterations)
	
	// Wire up metrics to evaluator for array operator tracking
	if b.base.Metrics != nil {
		// The evaluator needs access to metrics for array operator tracking
		// This is done internally when evaluator is created within processor
	}
	
	if err := b.base.Processor.LoadRules(rules); err != nil {
		b.err = fmt.Errorf("failed to load rules into processor: %w", err)
		return b
	}

	b.base.Logger.Info("rules loaded successfully", 
		"totalRules", len(rules),
		"maxForEachIterations", b.cfg.ForEach.MaxIterations)
	
	return b
}

// Build finalizes the construction and returns the BaseApp.
func (b *AppBuilder) Build() (*BaseApp, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.base, nil
}

// ShutdownMetricsServer gracefully shuts down the metrics server and waits for the goroutine to exit.
func (base *BaseApp) ShutdownMetricsServer(ctx context.Context) error {
	if base.MetricsServer == nil {
		return nil
	}

	// Shutdown the HTTP server (this causes ListenAndServe to return)
	if err := base.MetricsServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown metrics server: %w", err)
	}

	// Wait for the server goroutine to finish
	done := make(chan struct{})
	go func() {
		base.metricsServerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for metrics server goroutine: %w", ctx.Err())
	}
}

