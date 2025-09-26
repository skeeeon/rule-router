//file: internal/app/setup.go

package app

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"rule-router/internal/broker"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// setupLogger initializes the application logger
func (a *App) setupLogger() error {
	var err error
	a.logger, err = logger.NewLogger(&a.config.Logging)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}
	return nil
}

// setupMetrics initializes the metrics system and HTTP server
func (a *App) setupMetrics() error {
	if !a.config.Metrics.Enabled {
		a.logger.Info("metrics disabled")
		return nil
	}

	// Initialize metrics registry
	reg := prometheus.NewRegistry()
	var err error
	a.metrics, err = metrics.NewMetrics(reg)
	if err != nil {
		return fmt.Errorf("failed to create metrics service: %w", err)
	}

	// Parse metrics update interval
	updateInterval, err := time.ParseDuration(a.config.Metrics.UpdateInterval)
	if err != nil {
		return fmt.Errorf("invalid metrics update interval: %w", err)
	}

	// Create and start metrics collector
	a.metricsCollector = metrics.NewMetricsCollector(a.metrics, updateInterval)
	a.metricsCollector.Start()

	// Setup HTTP metrics server
	if err := a.setupMetricsServer(reg); err != nil {
		return fmt.Errorf("failed to setup metrics server: %w", err)
	}

	a.logger.Info("metrics initialized successfully",
		"address", a.config.Metrics.Address,
		"path", a.config.Metrics.Path,
		"updateInterval", updateInterval)

	return nil
}

// setupMetricsServer creates and starts the HTTP metrics server
func (a *App) setupMetricsServer(reg *prometheus.Registry) error {
	mux := http.NewServeMux()
	mux.Handle(a.config.Metrics.Path, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
	}))

	a.httpServer = &http.Server{
		Addr:    a.config.Metrics.Address,
		Handler: mux,
	}

	// Start server in background
	go func() {
		a.logger.Info("starting metrics server",
			"address", a.config.Metrics.Address,
			"path", a.config.Metrics.Path)
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Error("metrics server error", "error", err)
		}
	}()

	return nil
}

// setupRules loads rules from directory and creates the processor with KV support
func (a *App) setupRules() error {
	// Load rules from directory with KV bucket validation
	kvBuckets := []string{}
	if a.config.KV.Enabled {
		kvBuckets = a.config.KV.Buckets
	}
	
	rulesLoader := rule.NewRulesLoader(a.logger, kvBuckets)
	rules, err := rulesLoader.LoadFromDirectory(a.rulesPath)
	if err != nil {
		return fmt.Errorf("failed to load rules: %w", err)
	}

	// Create KV context if enabled - now with local cache support
	var kvContext *rule.KVContext
	if a.config.KV.Enabled && a.broker != nil {
		kvStores := a.broker.GetKVStores()
		localKVCache := a.broker.GetLocalKVCache() // Get local cache from broker
		
		kvContext = rule.NewKVContext(kvStores, a.logger, localKVCache)
		
		a.logger.Info("KV context created with local cache support", 
			"bucketCount", len(kvStores),
			"localCacheEnabled", localKVCache.IsEnabled())
	} else {
		a.logger.Info("KV support disabled or NATS broker not ready")
	}

	// Create rule processor with KV context
	a.processor = rule.NewProcessor(a.logger, a.metrics, kvContext)

	// Load rules into processor
	if err := a.processor.LoadRules(rules); err != nil {
		return fmt.Errorf("failed to load rules into processor: %w", err)
	}

	a.logger.Info("rules loaded successfully",
		"ruleCount", len(rules),
		"kvEnabled", a.config.KV.Enabled,
		"localCacheEnabled", func() bool {
			if kvContext != nil {
				stats := kvContext.GetStats()
				if cacheStats, ok := stats["local_cache"].(map[string]interface{}); ok {
					if enabled, ok := cacheStats["enabled"].(bool); ok {
						return enabled
					}
				}
			}
			return false
		}())

	return nil
}

// setupNATSBroker creates the NATS broker connection and initializes KV cache
func (a *App) setupNATSBroker() error {
	a.logger.Info("connecting to NATS JetStream server", "urls", a.config.NATS.URLs)
	
	natsBroker, err := broker.NewNATSBroker(a.config, a.logger, a.metrics)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS JetStream server: %w", err)
	}
	a.broker = natsBroker
	
	// Initialize local KV cache if KV is enabled
	if a.config.KV.Enabled {
		a.logger.Info("initializing local KV cache", 
			"buckets", a.config.KV.Buckets,
			"localCacheEnabled", a.config.KV.LocalCache.Enabled)
		
		if err := a.broker.InitializeKVCache(); err != nil {
			// Don't fail startup for KV cache issues - log error and continue
			// This allows the system to operate with degraded performance rather than failing
			a.logger.Error("failed to initialize local KV cache", "error", err)
			a.logger.Info("continuing with direct NATS KV access (degraded performance)")
		} else {
			// Log successful cache initialization with stats
			localCache := a.broker.GetLocalKVCache()
			if localCache != nil && localCache.IsEnabled() {
				stats := localCache.GetStats()
				a.logger.Info("local KV cache initialized successfully", "stats", stats)
			}
		}
	}
	
	// Log NATS connection results
	if a.config.KV.Enabled {
		kvStores := a.broker.GetKVStores()
		localCache := a.broker.GetLocalKVCache()
		
		a.logger.Info("NATS JetStream connected with KV support", 
			"kvBuckets", len(kvStores),
			"configuredBuckets", a.config.KV.Buckets,
			"localCacheEnabled", localCache != nil && localCache.IsEnabled())
	} else {
		a.logger.Info("NATS JetStream connected without KV support")
	}
	
	return nil
}

// setupRouter configures the Watermill router with middleware and handlers
func (a *App) setupRouter() error {
	// Get router from broker
	a.router = a.broker.GetRouter()

	// Setup middleware stack
	a.setupMiddleware()

	// Setup message handlers - now they do the real work
	a.setupHandlers()

	a.logger.Info("router configured successfully",
		"middlewareCount", "operational stack",
		"handlerCount", len(a.processor.GetSubjects()))

	return nil
}
