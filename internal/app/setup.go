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

// setupRules loads rules from directory and creates the processor
func (a *App) setupRules() error {
	// Load rules from directory
	rulesLoader := rule.NewRulesLoader(a.logger)
	rules, err := rulesLoader.LoadFromDirectory(a.rulesPath)
	if err != nil {
		return fmt.Errorf("failed to load rules: %w", err)
	}

	// Create rule processor
	processorCfg := rule.ProcessorConfig{
		Workers:   a.config.Processing.Workers,
		QueueSize: a.config.Processing.QueueSize,
		BatchSize: a.config.Processing.BatchSize,
	}
	a.processor = rule.NewProcessor(processorCfg, a.logger, a.metrics)

	// Load rules into processor
	if err := a.processor.LoadRules(rules); err != nil {
		return fmt.Errorf("failed to load rules into processor: %w", err)
	}

	a.logger.Info("rules loaded successfully",
		"ruleCount", len(rules),
		"workers", processorCfg.Workers,
		"queueSize", processorCfg.QueueSize,
		"batchSize", processorCfg.BatchSize)

	return nil
}

// setupBroker creates the appropriate Watermill broker based on configuration
func (a *App) setupBroker() error {
	switch a.config.BrokerType {
	case "nats":
		a.logger.Info("connecting to external NATS JetStream server", "urls", a.config.NATS.URLs)
		watermillBroker, err := broker.NewWatermillNATSBroker(a.config, a.logger, a.metrics)
		if err != nil {
			return fmt.Errorf("failed to connect to NATS JetStream server: %w", err)
		}
		a.broker = watermillBroker
		a.logger.Info("successfully connected to NATS JetStream")

	case "mqtt":
		a.logger.Info("connecting to external MQTT broker", "broker", a.config.MQTT.Broker)
		watermillBroker, err := broker.NewWatermillMQTTBroker(a.config, a.logger, a.metrics)
		if err != nil {
			return fmt.Errorf("failed to connect to MQTT broker: %w", err)
		}
		a.broker = watermillBroker
		a.logger.Info("successfully connected to MQTT broker")

	default:
		return fmt.Errorf("unsupported broker type: %s", a.config.BrokerType)
	}

	return nil
}

// setupRouter configures the Watermill router with middleware and handlers
func (a *App) setupRouter() error {
	// Get router from broker
	a.router = a.broker.GetRouter()

	// Setup middleware stack
	a.setupMiddleware()

	// Setup message handlers
	a.setupHandlers()

	a.logger.Info("router configured successfully",
		"middlewareCount", "full stack",
		"handlerCount", len(a.processor.GetTopics()))

	return nil
}
