// file: internal/app/scheduler.go

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/go-co-op/gocron/v2"
	"rule-router/config"
	"rule-router/internal/httpclient"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Timeout constants for SchedulerApp operations
const (
	// publishTimeout is the maximum time to wait for a single publish operation
	publishTimeout = 10 * time.Second
)

// kvScheduleTag is used to tag cron jobs loaded from KV so they can be
// removed as a group when KV rules change without affecting file-loaded jobs.
const kvScheduleTag = "kv-rule"

// Verify SchedulerApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*SchedulerApp)(nil)

// SchedulerApp represents the rule-scheduler application that publishes messages on cron schedules
type SchedulerApp struct {
	config       *config.Config
	logger       *logger.Logger
	metrics      *metrics.Metrics
	processor    *rule.Processor
	httpExecutor *httpclient.HTTPExecutor
	base         *BaseApp
	scheduler    gocron.Scheduler
}

// NewSchedulerApp creates a new rule-scheduler application instance using the pre-built base components.
// In KV mode, the caller must have already started base.RuleKVManager via the builder.
func NewSchedulerApp(base *BaseApp, cfg *config.Config) (*SchedulerApp, error) {
	app := &SchedulerApp{
		config:       cfg,
		logger:       base.Logger.With("component", "scheduler"),
		metrics:      base.Metrics,
		processor:    base.Processor,
		httpExecutor: httpclient.NewHTTPExecutor(&cfg.HTTP.Client, base.Logger, base.Metrics),
		base:         base,
	}

	scheduleRules := app.processor.GetScheduleRules()

	if len(scheduleRules) == 0 {
		if cfg.KV.Rules.Enabled {
			app.logger.Info("no schedule rules loaded yet (KV mode: rules will arrive via KV watch)")
		} else {
			return nil, fmt.Errorf("no schedule-triggered rules found")
		}
	}

	// Create gocron scheduler
	s, err := gocron.NewScheduler()
	if err != nil {
		return nil, fmt.Errorf("failed to create scheduler: %w", err)
	}
	app.scheduler = s

	// Register each schedule rule as a cron job
	for _, r := range scheduleRules {
		if err := app.registerScheduleRule(r); err != nil {
			return nil, fmt.Errorf("failed to register schedule rule (cron=%s): %w", r.Trigger.Schedule.Cron, err)
		}
	}

	app.logger.Info("schedule rules registered", "count", len(scheduleRules))

	// Register KV callback so cron jobs are rebuilt when rules change
	if cfg.KV.Rules.Enabled {
		base.RuleKVManager.SetScheduleRebuildFunc(app.rebuildCronJobs)
	}

	return app, nil
}

// registerScheduleRule registers a single schedule-triggered rule as a cron job.
// Extra gocron options can be passed (e.g., WithTags for KV-sourced rules).
func (app *SchedulerApp) registerScheduleRule(r *rule.Rule, opts ...gocron.JobOption) error {
	schedule := r.Trigger.Schedule

	// Build cron expression with timezone prefix if specified
	cronExpr := schedule.Cron
	if schedule.Timezone != "" {
		cronExpr = fmt.Sprintf("CRON_TZ=%s %s", schedule.Timezone, schedule.Cron)
	}

	// Capture rule for closure
	capturedRule := r

	_, err := app.scheduler.NewJob(
		gocron.CronJob(cronExpr, false), // false = standard 5-field cron (no seconds)
		gocron.NewTask(func() {
			app.executeScheduleRule(capturedRule)
		}),
		opts...,
	)
	if err != nil {
		return fmt.Errorf("failed to register cron job: %w", err)
	}

	app.logger.Info("registered schedule rule",
		"cron", cronExpr)

	return nil
}

// rebuildCronJobs removes all existing cron jobs and registers the provided rules.
// Called by RuleKVManager whenever schedule rules change in the KV bucket.
// gocron supports adding/removing jobs on a running scheduler, so no restart needed.
func (app *SchedulerApp) rebuildCronJobs(rules []*rule.Rule) {
	// Remove all existing KV-sourced jobs, then re-register
	app.scheduler.RemoveByTags(kvScheduleTag)

	for _, r := range rules {
		if err := app.registerScheduleRule(r, gocron.WithTags(kvScheduleTag)); err != nil {
			app.logger.Error("failed to register schedule rule during rebuild",
				"cron", r.Trigger.Schedule.Cron, "error", err)
		}
	}

	app.logger.Info("cron jobs rebuilt from KV rules", "count", len(rules))

	if app.metrics != nil {
		app.metrics.SetRulesActive(float64(len(rules)))
	}
}

// executeScheduleRule processes a schedule-triggered rule and publishes resulting actions
func (app *SchedulerApp) executeScheduleRule(r *rule.Rule) {
	app.logger.Debug("executing schedule rule", "cron", r.Trigger.Schedule.Cron)

	actions, err := app.processor.ProcessSchedule(r)
	if err != nil {
		app.logger.Error("failed to process schedule rule",
			"cron", r.Trigger.Schedule.Cron,
			"error", err)
		return
	}

	if len(actions) == 0 {
		app.logger.Debug("schedule rule produced no actions (conditions not met)",
			"cron", r.Trigger.Schedule.Cron)
		return
	}

	for _, action := range actions {
		if action.NATS != nil {
			ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
			if err := app.base.Broker.Publish(ctx, action.NATS); err != nil {
				app.logger.Error("failed to publish scheduled NATS action",
					"subject", action.NATS.Subject,
					"error", err)
				if app.metrics != nil {
					app.metrics.IncActionPublishFailures()
				}
			} else {
				app.logger.Info("published scheduled NATS action",
					"subject", action.NATS.Subject)
			}
			cancel()
		}

		if action.HTTP != nil {
			ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
			if err := app.httpExecutor.ExecuteHTTPAction(ctx, action.HTTP); err != nil {
				app.logger.Error("failed to execute scheduled HTTP action",
					"url", action.HTTP.URL,
					"method", action.HTTP.Method,
					"error", err)
				if app.metrics != nil {
					app.metrics.IncActionPublishFailures()
				}
			} else {
				app.logger.Info("executed scheduled HTTP action",
					"url", action.HTTP.URL,
					"method", action.HTTP.Method)
			}
			cancel()
		}
	}
}

// Run starts the scheduler and waits for shutdown signal.
func (app *SchedulerApp) Run(ctx context.Context) error {
	scheduleRules := app.processor.GetScheduleRules()

	app.logger.Info("configuration summary",
		"scheduleRules", len(scheduleRules),
		"kvEnabled", app.config.KV.Enabled,
		"kvBuckets", app.config.KV.BucketNames(),
		"publishMode", app.config.NATS.Publish.Mode)

	app.logger.Info("starting rule-scheduler",
		"urls", app.config.NATS.URLs,
		"metricsEnabled", app.config.Metrics.Enabled)

	// Start the cron scheduler. gocron's Start() is idempotent — safe to call
	// even if rebuildCronJobs has already been invoked during initial KV sync.
	app.scheduler.Start()

	app.logger.Info("scheduler started, all cron jobs active")

	// Update metrics
	if app.metrics != nil {
		app.metrics.SetRulesActive(float64(len(scheduleRules)))
	}

	// Wait for shutdown signal
	<-ctx.Done()
	app.logger.Info("shutting down gracefully...")

	// Stop the scheduler (waits for running jobs to finish)
	if err := app.scheduler.Shutdown(); err != nil {
		app.logger.Error("failed to shutdown scheduler", "error", err)
		return err
	}

	app.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down scheduler-specific resources.
// Shared resources (metrics, broker, logger) are cleaned up by BaseApp.Close().
func (app *SchedulerApp) Close() error {
	app.logger.Info("closing scheduler components")
	return nil
}
