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
	// Command line flags for config and rules
	configPath := flag.String("config", "config/config.yaml", "path to config file (YAML or JSON)")
	rulesPath := flag.String("rules", "rules", "path to rules directory")

	// Add broker type flag
	brokerTypeFlag := flag.String("broker-type", "", "broker type (mqtt or nats)")

	// Optional override flags
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

	// Apply any command line overrides
	cfg.ApplyOverrides(
		*brokerTypeFlag,
		*workersOverride,
		*queueSizeOverride,
		*batchSizeOverride,
		*metricsAddrOverride,
		*metricsPathOverride,
		*metricsIntervalOverride,
	)

	// Initialize logger
	appLogger, err := logger.NewLogger(&cfg.Logging)
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	defer appLogger.Sync()

	// Setup metrics if enabled
	var metricsService *metrics.Metrics
	var metricsCollector *metrics.MetricsCollector
	var metricsServer *http.Server

	if cfg.Metrics.Enabled {
		// Initialize metrics
		reg := prometheus.NewRegistry()
		metricsService, err = metrics.NewMetrics(reg)
		if err != nil {
			appLogger.Fatal("failed to create metrics service", "error", err)
		}

		// Parse metrics update interval
		updateInterval, err := time.ParseDuration(cfg.Metrics.UpdateInterval)
		if err != nil {
			appLogger.Fatal("invalid metrics update interval", "error", err)
		}

		// Create metrics collector
		metricsCollector = metrics.NewMetricsCollector(metricsService, updateInterval)
		metricsCollector.Start()
		defer metricsCollector.Stop()

		// Setup metrics HTTP server
		mux := http.NewServeMux()
		mux.Handle(cfg.Metrics.Path, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
			Registry:          reg,
			EnableOpenMetrics: true,
		}))

		metricsServer = &http.Server{
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
	}

	// Setup signal handlers
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load rules from directory
	rulesLoader := rule.NewRulesLoader(appLogger)
	rules, err := rulesLoader.LoadFromDirectory(*rulesPath)
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

	// Create message handler
	messageHandler := handler.NewMessageHandler(processor, appLogger, metricsService)

	// Create Watermill broker connections to external brokers
	var watermillBroker interface {
		GetPublisher() message.Publisher
		GetSubscriber() message.Subscriber
		GetRouter() *message.Router
		Close() error
	}

	switch cfg.BrokerType {
	case "nats":
		appLogger.Info("connecting to external NATS JetStream server",
			"urls", cfg.NATS.URLs)
		watermillBroker, err = broker.NewWatermillNATSBroker(cfg, appLogger, metricsService)
		if err != nil {
			appLogger.Fatal("failed to connect to NATS JetStream server", "error", err)
		}
	case "mqtt":
		appLogger.Info("connecting to external MQTT broker",
			"broker", cfg.MQTT.Broker)
		watermillBroker, err = broker.NewWatermillMQTTBroker(cfg, appLogger, metricsService)
		if err != nil {
			appLogger.Fatal("failed to connect to MQTT broker", "error", err)
		}
	default:
		appLogger.Fatal("unsupported broker type", "type", cfg.BrokerType)
	}

	// Get components from broker
	publisher := watermillBroker.GetPublisher()
	subscriber := watermillBroker.GetSubscriber()
	router := watermillBroker.GetRouter()

	// Add comprehensive middleware stack
	router.AddMiddleware(
		// Correlation ID propagation
		middleware.CorrelationID,
		
		// Custom middleware
		handler.CorrelationIDMiddleware(),
		handler.LoggingMiddleware(appLogger),
		handler.ValidationMiddleware(appLogger),
		handler.RecoveryMiddleware(appLogger),
		
		// Conditional metrics middleware
		func() message.HandlerMiddleware {
			if cfg.Watermill.Middleware.MetricsEnabled && metricsService != nil {
				return handler.MetricsMiddleware(metricsService)
			}
			return func(h message.HandlerFunc) message.HandlerFunc {
				return h // Pass-through if metrics disabled
			}
		}(),
		
		// Retry middleware with configuration
		handler.RetryMiddleware(cfg.Watermill.Middleware.RetryMaxAttempts, appLogger),
		
		// Poison queue handling
		handler.PoisonQueueMiddleware(appLogger, metricsService),
		
		// Rule engine integration (this is where the magic happens)
		handler.RuleEngineMiddleware(messageHandler),
	)

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

	// Start the router
	appLogger.Info("starting Watermill router with external broker connections",
		"brokerType", cfg.BrokerType,
		"externalBroker", getExternalBrokerInfo(cfg),
		"workers", cfg.Processing.Workers,
		"rulesCount", len(rules),
		"topicsCount", len(topics),
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
		metricsService.SetRulesActive(float64(len(rules)))
		metricsService.SetWorkerPoolActive(float64(cfg.Processing.Workers))
	}

	// Handle signals
	for {
		sig := <-sigChan
		switch sig {
		case syscall.SIGHUP:
			appLogger.Info("received SIGHUP, reopening logs")
			appLogger.Sync()
		case syscall.SIGINT, syscall.SIGTERM:
			appLogger.Info("shutting down...")

			// Graceful shutdown
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()

			// Shutdown metrics server if enabled
			if cfg.Metrics.Enabled && metricsServer != nil {
				if err := metricsServer.Shutdown(shutdownCtx); err != nil {
					appLogger.Error("failed to shutdown metrics server", "error", err)
				}
			}

			// Shutdown Watermill components and close external broker connections
			cancel() // Cancel main context
			
			if err := router.Close(); err != nil {
				appLogger.Error("failed to close router", "error", err)
			}

			if err := watermillBroker.Close(); err != nil {
				appLogger.Error("failed to close external broker connections", "error", err)
			}

			// Close processor
			processor.Close()

			appLogger.Info("shutdown complete")
			return
		}
	}
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
	sanitized = replaceString(sanitized, "/", "-")
	sanitized = replaceString(sanitized, ".", "-")
	sanitized = replaceString(sanitized, " ", "-")
	sanitized = replaceString(sanitized, "#", "wildcard")
	sanitized = replaceString(sanitized, "+", "single")
	return sanitized
}

// toNATSSubject converts MQTT topic format to NATS subject format
func toNATSSubject(mqttTopic string) string {
	// Convert MQTT topic format to NATS subject format
	// MQTT uses / as separators and +/# as wildcards
	// NATS uses . as separators and */> as wildcards
	subject := mqttTopic
	subject = replaceString(subject, "+", "*")
	subject = replaceString(subject, "#", ">")
	subject = replaceString(subject, "/", ".")
	return subject
}

// replaceString is a simple string replacement function
func replaceString(s, old, new string) string {
	result := ""
	i := 0
	for i < len(s) {
		if i <= len(s)-len(old) && s[i:i+len(old)] == old {
			result += new
			i += len(old)
		} else {
			result += string(s[i])
			i++
		}
	}
	return result
}
