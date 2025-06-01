//file: cmd/rule-router/main.go

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/handler"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

func main() {
	// Parse command line flags
	cfg, rulesPath := parseFlags()

	// Initialize logger
	appLogger := initializeLogger(cfg)
	defer appLogger.Sync()

	// Setup metrics and HTTP server
	metricsService, metricsCollector, metricsServer := setupMetrics(cfg, appLogger)
	defer cleanupMetrics(metricsCollector, metricsServer, cfg)

	// Setup signal handling
	ctx, cancel := setupSignalHandling()
	defer cancel()

	// Load and process rules
	processor := loadAndProcessRules(*rulesPath, cfg, appLogger, metricsService)
	defer processor.Close()

	// Create message handler
	messageHandler := handler.NewMessageHandler(processor, appLogger, metricsService)

	// Create and configure Watermill broker
	watermillBroker := createWatermillBroker(cfg, appLogger, metricsService)
	defer watermillBroker.Close()

	// Setup router with middleware and handlers
	router := watermillBroker.GetRouter()
	setupMiddleware(router, cfg, messageHandler, appLogger, metricsService)
	setupHandlers(router, processor, watermillBroker, cfg, appLogger)

	// Start the router and wait
	startRouterAndWait(ctx, router, cfg, processor, appLogger, metricsService)
}

// parseFlags parses command line arguments and applies overrides
func parseFlags() (*config.Config, *string) {
	configPath := flag.String("config", "config/config.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")
	brokerTypeFlag := flag.String("broker-type", "", "broker type (mqtt or nats)")
	workersOverride := flag.Int("workers", 0, "override number of worker threads (0 = use config)")
	queueSizeOverride := flag.Int("queue-size", 0, "override size of processing queue (0 = use config)")
	batchSizeOverride := flag.Int("batch-size", 0, "override message batch size (0 = use config)")
	metricsAddrOverride := flag.String("metrics-addr", "", "override metrics server address (empty = use config)")
	metricsPathOverride := flag.String("metrics-path", "", "override metrics endpoint path (empty = use config)")
	metricsIntervalOverride := flag.Duration("metrics-interval", 0, "override metrics collection interval (0 = use config)")

	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Apply command line overrides
	cfg.ApplyOverrides(
		*brokerTypeFlag,
		*workersOverride,
		*queueSizeOverride,
		*batchSizeOverride,
		*metricsAddrOverride,
		*metricsPathOverride,
		*metricsIntervalOverride,
	)

	return cfg, rulesPath
}

// initializeLogger creates and configures the application logger
func initializeLogger(cfg *config.Config) *logger.Logger {
	appLogger, err := logger.NewLogger(&cfg.Logging)
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	return appLogger
}

// setupMetrics initializes metrics service, collector, and HTTP server
func setupMetrics(cfg *config.Config, appLogger *logger.Logger) (*metrics.Metrics, *metrics.MetricsCollector, *http.Server) {
	if !cfg.Metrics.Enabled {
		return nil, nil, nil
	}

	// Initialize metrics
	reg := prometheus.NewRegistry()
	metricsService, err := metrics.NewMetrics(reg)
	if err != nil {
		appLogger.Fatal("failed to create metrics service", "error", err)
	}

	// Parse metrics update interval
	updateInterval, err := time.ParseDuration(cfg.Metrics.UpdateInterval)
	if err != nil {
		appLogger.Fatal("invalid metrics update interval", "error", err)
	}

	// Create metrics collector
	metricsCollector := metrics.NewMetricsCollector(metricsService, updateInterval)
	metricsCollector.Start()

	// Setup metrics HTTP server
	mux := http.NewServeMux()
	mux.Handle(cfg.Metrics.Path, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
	}))

	metricsServer := &http.Server{
		Addr:    cfg.Metrics.Address,
		Handler: mux,
	}

	// Start metrics server
	go func() {
		appLogger.Info("starting metrics server",
			"address", cfg.Metrics.Address,
			"path", cfg.Metrics.Path)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			appLogger.Error("metrics server error", "error", err)
		}
	}()

	return metricsService, metricsCollector, metricsServer
}

// cleanupMetrics gracefully shuts down metrics components
func cleanupMetrics(metricsCollector *metrics.MetricsCollector, metricsServer *http.Server, cfg *config.Config) {
	if !cfg.Metrics.Enabled {
		return
	}

	if metricsCollector != nil {
		metricsCollector.Stop()
	}

	if metricsServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			// Log error but don't fail - this is cleanup
			log.Printf("failed to shutdown metrics server gracefully: %v", err)
		}
	}
}

// setupSignalHandling configures OS signal handling
func setupSignalHandling() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	
	go func() {
		for sig := range sigChan {
			switch sig {
			case syscall.SIGHUP:
				// Handle log rotation - could add logger reopening here
				continue
			case syscall.SIGINT, syscall.SIGTERM:
				cancel()
				return
			}
		}
	}()
	
	return ctx, cancel
}

// loadAndProcessRules loads rules from directory and creates processor
func loadAndProcessRules(rulesPath string, cfg *config.Config, appLogger *logger.Logger, metricsService *metrics.Metrics) *rule.Processor {
	// Load rules from directory
	rulesLoader := rule.NewRulesLoader(appLogger)
	rules, err := rulesLoader.LoadFromDirectory(rulesPath)
	if err != nil {
		appLogger.Fatal("failed to load rules", "error", err)
	}

	// Create rule processor
	processorCfg := rule.ProcessorConfig{
		Workers:   cfg.Processing.Workers,
		QueueSize: cfg.Processing.QueueSize,
		BatchSize: cfg.Processing.BatchSize,
	}
	processor := rule.NewProcessor(processorCfg, appLogger, metricsService)

	// Load rules into processor
	if err := processor.LoadRules(rules); err != nil {
		appLogger.Fatal("failed to load rules into processor", "error", err)
	}

	return processor
}

// createWatermillBroker creates the appropriate Watermill broker based on configuration
func createWatermillBroker(cfg *config.Config, appLogger *logger.Logger, metricsService *metrics.Metrics) interface {
	GetPublisher() message.Publisher
	GetSubscriber() message.Subscriber
	GetRouter() *message.Router
	Close() error
} {
	switch cfg.BrokerType {
	case "nats":
		appLogger.Info("connecting to external NATS JetStream server", "urls", cfg.NATS.URLs)
		watermillBroker, err := broker.NewWatermillNATSBroker(cfg, appLogger, metricsService)
		if err != nil {
			appLogger.Fatal("failed to connect to NATS JetStream server", "error", err)
		}
		return watermillBroker
	case "mqtt":
		appLogger.Info("connecting to external MQTT broker", "broker", cfg.MQTT.Broker)
		watermillBroker, err := broker.NewWatermillMQTTBroker(cfg, appLogger, metricsService)
		if err != nil {
			appLogger.Fatal("failed to connect to MQTT broker", "error", err)
		}
		return watermillBroker
	default:
		appLogger.Fatal("unsupported broker type", "type", cfg.BrokerType)
		return nil
	}
}

// setupMiddleware configures the Watermill router middleware stack
func setupMiddleware(router *message.Router, cfg *config.Config, messageHandler *handler.MessageHandler, appLogger *logger.Logger, metricsService *metrics.Metrics) {
	router.AddMiddleware(
		// Correlation ID propagation
		middleware.CorrelationID,
		
		// Custom middleware
		handler.CorrelationIDMiddleware(),
		handler.LoggingMiddleware(appLogger),
		handler.ValidationMiddleware(appLogger),
		handler.RecoveryMiddleware(appLogger),
		
		// Conditional metrics middleware
		conditionalMetricsMiddleware(cfg, metricsService),
		
		// Retry middleware with configuration
		handler.RetryMiddleware(cfg.Watermill.Middleware.RetryMaxAttempts, appLogger),
		
		// Poison queue handling
		handler.PoisonQueueMiddleware(appLogger, metricsService),
		
		// Rule engine integration (this is where the magic happens)
		handler.RuleEngineMiddleware(messageHandler),
	)
}

// conditionalMetricsMiddleware returns metrics middleware if enabled, otherwise pass-through
func conditionalMetricsMiddleware(cfg *config.Config, metricsService *metrics.Metrics) message.HandlerMiddleware {
	if cfg.Watermill.Middleware.MetricsEnabled && metricsService != nil {
		return handler.MetricsMiddleware(metricsService)
	}
	return func(h message.HandlerFunc) message.HandlerFunc {
		return h // Pass-through if metrics disabled
	}
}

// setupHandlers configures message handlers for each rule topic
func setupHandlers(router *message.Router, processor *rule.Processor, watermillBroker interface {
	GetPublisher() message.Publisher
	GetSubscriber() message.Subscriber
}, cfg *config.Config, appLogger *logger.Logger) {
	
	publisher := watermillBroker.GetPublisher()
	subscriber := watermillBroker.GetSubscriber()
	
	// Extract unique topics from rules and add handlers
	topics := processor.GetTopics()
	appLogger.Info("setting up Watermill handlers for rule topics", "topicCount", len(topics))

	for _, topic := range topics {
		handlerName := fmt.Sprintf("processor-%s", sanitizeHandlerName(topic))
		
		// Convert MQTT topic format to NATS subject format if using NATS
		subscribeTopic := topic
		publishTopic := "processed." + topic
		
		if cfg.BrokerType == "nats" {
			subscribeTopic = toNATSSubject(topic)
			publishTopic = toNATSSubject(publishTopic)
		}

		appLogger.Debug("adding handler",
			"handlerName", handlerName,
			"subscribeTopic", subscribeTopic,
			"publishTopic", publishTopic)

		router.AddHandler(
			handlerName,
			subscribeTopic,
			subscriber,
			publishTopic,
			publisher,
			func(msg *message.Message) ([]*message.Message, error) {
				// Set topic in metadata for rule processing
				msg.Metadata.Set("topic", topic)
				
				// The actual processing happens in the middleware chain
				// This handler just passes the message through
				return []*message.Message{msg}, nil
			},
		)
	}
}

// startRouterAndWait starts the router and waits for shutdown
func startRouterAndWait(ctx context.Context, router *message.Router, cfg *config.Config, processor *rule.Processor, appLogger *logger.Logger, metricsService *metrics.Metrics) {
	// Start the router
	appLogger.Info("starting Watermill router with external broker connections",
		"brokerType", cfg.BrokerType,
		"externalBroker", getExternalBrokerInfo(cfg),
		"workers", cfg.Processing.Workers,
		"topicsCount", len(processor.GetTopics()),
		"metricsEnabled", cfg.Metrics.Enabled)

	go func() {
		if err := router.Run(ctx); err != nil {
			appLogger.Error("router stopped with error", "error", err)
		}
	}()

	// Wait for router to be ready
	<-router.Running()
	appLogger.Info("Watermill router is running and ready to process messages")

	// Update metrics
	if metricsService != nil {
		topics := processor.GetTopics()
		metricsService.SetRulesActive(float64(len(topics)))
		metricsService.SetWorkerPoolActive(float64(cfg.Processing.Workers))
	}

	// Wait for shutdown signal
	<-ctx.Done()
	appLogger.Info("shutting down gracefully...")

	// Close the router (no additional shutdown context needed since we already have graceful shutdown via ctx)
	if err := router.Close(); err != nil {
		appLogger.Error("failed to close router", "error", err)
	}

	appLogger.Info("shutdown complete")
}

// getExternalBrokerInfo returns broker connection info for logging
func getExternalBrokerInfo(cfg *config.Config) string {
	switch cfg.BrokerType {
	case "nats":
		if len(cfg.NATS.URLs) > 0 {
			return cfg.NATS.URLs[0]
		}
		return "nats://localhost:4222"
	case "mqtt":
		return cfg.MQTT.Broker
	default:
		return "unknown"
	}
}

// sanitizeHandlerName ensures handler names are valid for Watermill
func sanitizeHandlerName(topic string) string {
	// Replace problematic characters in topic names for handler names
	sanitized := topic
	sanitized = strings.ReplaceAll(sanitized, "/", "-")
	sanitized = strings.ReplaceAll(sanitized, ".", "-")
	sanitized = strings.ReplaceAll(sanitized, " ", "-")
	sanitized = strings.ReplaceAll(sanitized, "#", "wildcard")
	sanitized = strings.ReplaceAll(sanitized, "+", "single")
	return sanitized
}

// toNATSSubject converts MQTT topic format to NATS subject format
func toNATSSubject(mqttTopic string) string {
	// Convert MQTT topic format to NATS subject format
	// MQTT uses / as separators and +/# as wildcards
	// NATS uses . as separators and */> as wildcards
	subject := mqttTopic
	subject = strings.ReplaceAll(subject, "+", "*")
	subject = strings.ReplaceAll(subject, "#", ">")
	subject = strings.ReplaceAll(subject, "/", ".")
	return subject
}
