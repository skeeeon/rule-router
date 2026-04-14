// file: internal/app/router.go

package app

import (
	"context"
	"fmt"
	"time"

	"rule-router/config"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Timeout constants for RouterApp operations
const (
	// subscriptionSetupTimeout is the maximum time to wait for all subscriptions to be configured
	subscriptionSetupTimeout = 60 * time.Second
)

// Verify RouterApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*RouterApp)(nil)

// RouterApp represents the rule-router application with all its components
type RouterApp struct {
	config    *config.Config
	logger    *logger.Logger
	metrics   *metrics.Metrics
	processor *rule.Processor
	base      *BaseApp
}

// NewRouterApp creates a new rule-router application instance using the pre-built base components.
// In KV mode, the caller must have already started base.RuleKVManager via the builder.
func NewRouterApp(base *BaseApp, cfg *config.Config) (*RouterApp, error) {
	app := &RouterApp{
		config:    cfg,
		logger:    base.Logger.With("component", "router"),
		metrics:   base.Metrics,
		processor: base.Processor,
		base:      base,
	}

	// Initialize subscription manager with processor
	base.Broker.InitializeSubscriptionManager(app.processor)

	if !cfg.KV.Rules.Enabled {
		// File-based mode: setup subscriptions for all rule subjects
		if err := app.setupSubscriptions(); err != nil {
			return nil, fmt.Errorf("failed to setup subscriptions: %w", err)
		}
	}
	// KV mode: subscriptions are managed by base.RuleKVManager (no router-specific callback needed)

	return app, nil
}

// Run starts the application and waits for shutdown signal.
// Note: Signal handling is managed by lifecycle.RunWithReload() - do not add signal handling here
// to avoid double registration which causes unpredictable behavior.
func (app *RouterApp) Run(ctx context.Context) error {
	// Get subscription count
	subMgr := app.base.Broker.GetSubscriptionManager()
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

// Close gracefully shuts down router-specific resources.
// Shared resources (metrics, broker, logger) are cleaned up by BaseApp.Close().
func (app *RouterApp) Close() error {
	app.logger.Info("closing router components")
	return nil
}

// setupSubscriptions configures JetStream consumers and subscriptions for all NATS-triggered rules.
// This is router-specific logic, moved from the old router_setup.go.
func (app *RouterApp) setupSubscriptions() error {
	subjects := app.processor.GetSubjects()
	app.logger.Info("setting up subscriptions for rule subjects", "subjectCount", len(subjects))

	if err := app.base.Broker.ValidateSubjects(subjects); err != nil {
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

		if err := app.base.Broker.CreateConsumerForSubject(subject); err != nil {
			return fmt.Errorf("failed to create consumer for subject '%s': %w", subject, err)
		}

		if err := app.base.Broker.AddSubscription(subject); err != nil {
			return fmt.Errorf("failed to add subscription for subject '%s': %w", subject, err)
		}

		app.logger.Info("subscription configured", "subject", subject)
	}

	app.logger.Info("all subscriptions configured successfully", "subscriptionCount", len(subjects))
	return nil
}


