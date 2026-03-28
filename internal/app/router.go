// file: internal/app/router.go

package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Timeout constants for RouterApp operations
const (
	// metricsShutdownTimeout is the maximum time to wait for the metrics server to shutdown
	metricsShutdownTimeout = 5 * time.Second

	// subscriptionSetupTimeout is the maximum time to wait for all subscriptions to be configured
	subscriptionSetupTimeout = 60 * time.Second
)

// Verify RouterApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*RouterApp)(nil)

// RouterApp represents the rule-router application with all its components including KV support
type RouterApp struct {
	config           *config.Config
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           *broker.NATSBroker
	base             *BaseApp // Reference to BaseApp for proper shutdown
	metricsCollector *metrics.MetricsCollector
	ruleKVManager    *broker.RuleKVManager
}

// NewRouterApp creates a new rule-router application instance using the pre-built base components.
func NewRouterApp(base *BaseApp, cfg *config.Config) (*RouterApp, error) {
	app := &RouterApp{
		config:           cfg,
		logger:           base.Logger.With("component", "router"),
		metrics:          base.Metrics,
		processor:        base.Processor,
		broker:           base.Broker,
		base:             base,
		metricsCollector: base.Collector,
	}

	// Initialize subscription manager with processor
	app.broker.InitializeSubscriptionManager(app.processor)

	if cfg.KV.Rules.Enabled {
		// KV rules mode: rules are loaded from KV bucket and watched for changes
		if err := app.setupKVRules(); err != nil {
			return nil, fmt.Errorf("failed to setup KV rules: %w", err)
		}
	} else {
		// File-based mode: setup subscriptions for all rule subjects
		if err := app.setupSubscriptions(); err != nil {
			return nil, fmt.Errorf("failed to setup subscriptions: %w", err)
		}
	}

	return app, nil
}

// setupKVRules initializes the KV rule manager, watches for rules, and waits for initial sync.
func (app *RouterApp) setupKVRules() error {
	kvCfg := app.config.KV.Rules

	app.ruleKVManager = broker.NewRuleKVManager(
		kvCfg.Bucket,
		kvCfg.AutoProvision,
		app.processor,
		app.broker,
		app.base.RulesLoader,
		app.logger,
	)

	if err := app.ruleKVManager.Watch(context.Background()); err != nil {
		return fmt.Errorf("failed to start KV rule watcher: %w", err)
	}

	// Block until initial KV sync completes
	ctx, cancel := context.WithTimeout(context.Background(), subscriptionSetupTimeout)
	defer cancel()

	if err := app.ruleKVManager.WaitReady(ctx); err != nil {
		return fmt.Errorf("timeout waiting for initial KV rule sync: %w", err)
	}

	app.logger.Info("KV rules loaded and subscriptions configured",
		"bucket", kvCfg.Bucket)

	return nil
}

// Run starts the application and waits for shutdown signal.
// Note: Signal handling is managed by lifecycle.RunWithReload() - do not add signal handling here
// to avoid double registration which causes unpredictable behavior.
func (app *RouterApp) Run(ctx context.Context) error {
	// Get subscription count
	subMgr := app.broker.GetSubscriptionManager()
	subCount := subMgr.GetSubscriptionCount()

	// Log configuration summary for transparency
	allRules := app.processor.GetAllRules()
	natsSubjects := app.processor.GetSubjects()
	app.logger.Info("configuration summary",
		"totalRules", len(allRules),
		"natsSubjects", len(natsSubjects),
		"subjectList", natsSubjects,
		"kvEnabled", app.config.KV.Enabled,
		"kvBuckets", app.config.KV.BucketNames(),
		"publishMode", app.config.NATS.Publish.Mode,
		"workerCount", app.config.NATS.Consumers.WorkerCount)

	app.logger.Info("starting rule-router with NATS JetStream",
		"urls", app.config.NATS.URLs,
		"subscriptionCount", subCount,
		"metricsEnabled", app.config.Metrics.Enabled)

	// Start subscription manager (only for file-based mode;
	// in KV mode, subscriptions are started dynamically by RuleKVManager)
	if !app.config.KV.Rules.Enabled {
		if err := subMgr.Start(ctx); err != nil {
			return fmt.Errorf("failed to start subscription manager: %w", err)
		}
	}

	app.logger.Info("all subscriptions active and processing messages")

	// Update metrics
	if app.metrics != nil {
		subjects := app.processor.GetAllRules()
		app.metrics.SetRulesActive(float64(len(subjects)))
	}

	// Wait for shutdown signal
	<-ctx.Done()
	app.logger.Info("shutting down gracefully...")

	// Stop subscription manager
	if err := subMgr.Stop(); err != nil {
		app.logger.Error("failed to stop subscription manager", "error", err)
		return err
	}

	app.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down all application components
func (app *RouterApp) Close() error {
	app.logger.Info("closing application components")

	var errs []error

	// Stop metrics collector
	if app.metricsCollector != nil {
		app.metricsCollector.Stop()
	}

	// Shutdown metrics server and wait for goroutine to exit
	if app.base != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
		defer cancel()
		if err := app.base.ShutdownMetricsServer(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
	}

	// Stop KV rule manager before broker
	if app.ruleKVManager != nil {
		app.ruleKVManager.Stop()
	}

	// Close NATS broker (this will stop subscriptions and clean up)
	if app.broker != nil {
		if err := app.broker.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close NATS broker: %w", err))
		}
	}

	// Sync logger
	if app.logger != nil {
		if err := app.logger.Sync(); err != nil {
			app.logger.Debug("logger sync completed", "error", err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %w", errors.Join(errs...))
	}

	return nil
}

// setupSubscriptions configures JetStream consumers and subscriptions for all NATS-triggered rules.
// This is router-specific logic, moved from the old router_setup.go.
func (app *RouterApp) setupSubscriptions() error {
	subjects := app.processor.GetSubjects()
	app.logger.Info("setting up subscriptions for rule subjects", "subjectCount", len(subjects))

	if err := app.broker.ValidateSubjects(subjects); err != nil {
		return fmt.Errorf("stream validation failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), subscriptionSetupTimeout)
	defer cancel()

	for _, subject := range subjects {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout during subscription setup for subject '%s'", subject)
		default:
		}

		app.logger.Debug("setting up subscription for subject", "subject", subject)

		if err := app.broker.CreateConsumerForSubject(subject); err != nil {
			return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
		}

		if err := app.broker.AddSubscription(subject); err != nil {
			return fmt.Errorf("failed to add subscription for subject '%s': %w", subject, err)
		}

		app.logger.Info("subscription configured", "subject", subject)
	}

	app.logger.Info("all subscriptions configured successfully", "subscriptionCount", len(subjects))
	return nil
}


