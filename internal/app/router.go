// file: internal/app/router.go

package app

import (
	"context"
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
	responder *broker.Responder
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
		responder: broker.NewResponder(base.Broker.GetNATSConn(), base.Processor, base.Logger, base.Metrics),
		base:      base,
	}

	if !cfg.KV.Rules.Enabled {
		// File-based mode: setup JetStream subscriptions for all non-reply rule
		// subjects, then bring up core-NATS responder subscriptions for reply rules.
		if err := app.setupSubscriptions(); err != nil {
			return nil, fmt.Errorf("failed to setup subscriptions: %w", err)
		}
		app.responder.Rebuild(app.processor.GetAllRules())
	} else {
		// KV mode: JetStream subscriptions are managed by base.RuleKVManager.
		// Register a callback so responder subscriptions track reply rule changes.
		base.RuleKVManager.SetReplyRebuildFunc(app.responder.Rebuild)
	}

	return app, nil
}

// Run starts the application and waits for shutdown signal.
// Note: Signal handling is managed by lifecycle.RunWithReload() - do not add signal handling here
// to avoid double registration which causes unpredictable behavior.
func (app *RouterApp) Run(ctx context.Context) error {
	// KV mode: deterministically establish responder subscriptions from the synced
	// rule set. The per-put rebuild callback races with KV watch startup (Watch()
	// begins draining before NewRouterApp registers the callback), so a reply rule
	// present at startup could otherwise be missed until the next KV change. This
	// bootstrap runs once initial sync is complete; the callback handles later changes.
	if app.config.KV.Rules.Enabled && app.base.RuleKVManager != nil {
		if err := app.base.RuleKVManager.WaitReady(ctx); err != nil {
			return err
		}
		app.responder.Rebuild(app.base.RuleKVManager.GetReplyRules())
	}

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

	app.logger.Info("all subscriptions active and processing messages")

	// Update metrics
	if app.metrics != nil {
		subjects := app.processor.GetAllRules()
		app.metrics.SetRulesActive(float64(len(subjects)))
	}

	// Wait for shutdown signal
	<-ctx.Done()
	app.logger.Info("shutting down gracefully...")

	app.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down router-specific resources.
// Shared resources (metrics, broker, logger) are cleaned up by BaseApp.Close().
func (app *RouterApp) Close() error {
	app.logger.Info("closing router components")
	if app.responder != nil {
		app.responder.Close()
	}
	return nil
}

// setupSubscriptions configures JetStream consumers and subscriptions for all NATS-triggered
// rules, excluding reply:true subjects (those are served by the core-NATS responder).
// This is router-specific logic, moved from the old router_setup.go.
func (app *RouterApp) setupSubscriptions() error {
	subjects := app.jetStreamSubjects()
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

// jetStreamSubjects returns the NATS trigger subjects that need JetStream
// consumers — i.e. all rule subjects except those served ONLY by reply:true
// rules (the core-NATS responder handles those). A subject is skipped only when
// no non-reply rule needs it, matching the per-rule semantics the KV path uses
// in collectNATSTriggerSubjects — otherwise a non-reply rule sharing a subject
// with a reply rule would silently lose its consumer.
func (app *RouterApp) jetStreamSubjects() []string {
	needsJetStream := make(map[string]bool) // subject has at least one non-reply rule
	replyOnly := make(map[string]bool)      // subject has at least one reply rule
	for _, r := range app.processor.GetAllRules() {
		if r.Trigger.NATS == nil {
			continue
		}
		if r.Trigger.NATS.Reply {
			replyOnly[r.Trigger.NATS.Subject] = true
		} else {
			needsJetStream[r.Trigger.NATS.Subject] = true
		}
	}

	var subjects []string
	for _, s := range app.processor.GetSubjects() {
		if replyOnly[s] && !needsJetStream[s] {
			app.logger.Info("subject served by core-NATS responder (reply:true), skipping JetStream", "subject", s)
			continue
		}
		subjects = append(subjects, s)
	}
	return subjects
}
