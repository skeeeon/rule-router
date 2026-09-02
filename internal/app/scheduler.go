package app

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/go-co-op/gocron/v2"
	"rule-router/config"
	"rule-router/internal/deferred"
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

	// deferredFlushTimeout bounds the shutdown flush of pending
	// trailing-throttle batches.
	deferredFlushTimeout = 10 * time.Second
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
	httpExecutor *httpclient.Executor
	base         *BaseApp
	scheduler    gocron.Scheduler

	// coalescer holds trailing-throttle actions from cron rules. Rarely useful
	// on a schedule trigger (cron already paces the firing) but supported for
	// consistency: any action that accepts a throttle accepts both modes.
	coalescer *deferred.Coalescer
}

// NewSchedulerApp creates a new rule-scheduler application instance using the pre-built base components.
// In KV mode, the caller must have already started base.RuleKVManager via the builder.
func NewSchedulerApp(base *BaseApp, cfg *config.Config) (*SchedulerApp, error) {
	app := &SchedulerApp{
		config:       cfg,
		logger:       base.Logger.With("component", "scheduler"),
		metrics:      base.Metrics,
		processor:    base.Processor,
		httpExecutor: httpclient.NewExecutor(&cfg.HTTP.Client, base.Logger, base.Metrics, base.Broker),
		base:         base,
	}
	app.coalescer = deferred.New("scheduler", app.executeDeferred, publishTimeout, base.Logger, base.Metrics)

	scheduleRules := app.processor.ScheduleRules()

	if len(scheduleRules) == 0 {
		if cfg.KV.Rules.Enabled {
			app.logger.Info("no schedule rules loaded yet (KV mode: rules will arrive via KV watch)")
		} else {
			return nil, errors.New("no schedule-triggered rules found")
		}
	}

	// Create gocron scheduler. The monitor is what surfaces fires dropped by
	// singleton mode; see scheduler_monitor.go.
	s, err := gocron.NewScheduler(
		gocron.WithMonitor(newScheduleMonitor(app.logger, app.metrics)),
	)
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

	// LimitModeReschedule drops overlapping fires rather than queueing them, so a
	// slow rule (e.g. HTTP action against a hanging endpoint) can't stack up jobs
	// faster than they complete. Combined with the recover() below this keeps a
	// single misbehaving rule from taking the whole scheduler down. The drop is
	// silent inside gocron; scheduleMonitor is what makes it observable, which
	// matters now that a rule can fire every second.
	//
	// WithName carries the raw cron expression through to the monitor, which uses
	// it as the metric label and in its log lines.
	jobOpts := append([]gocron.JobOption{
		gocron.WithSingletonMode(gocron.LimitModeReschedule),
		gocron.WithName(schedule.Cron),
	}, opts...)

	_, err := app.scheduler.NewJob(
		// true = allow an optional leading seconds field. gocron's withSeconds
		// parser is SecondOptional-based, so 5-field expressions parse exactly as
		// before and 6-field ones ("*/5 * * * * *") become expressible.
		gocron.CronJob(cronExpr, true),
		gocron.NewTask(func() {
			defer func() {
				if r := recover(); r != nil {
					app.logger.Error("panic recovered in scheduled job",
						"panic", r,
						"cron", capturedRule.Trigger.Schedule.Cron,
						"stack", string(debug.Stack()))
					if app.metrics != nil {
						app.metrics.IncActionPublishFailures()
					}
				}
			}()
			app.executeScheduleRule(capturedRule)
		}),
		jobOpts...,
	)
	if err != nil {
		return fmt.Errorf("failed to register cron job: %w", err)
	}

	// Log the resolved cadence, not just the expression. "*/5 * * * *" and
	// "*/5 * * * * *" differ by one character and by a factor of 60, and both are
	// valid, so no validator can catch the typo — the interval in this line is
	// what makes a runaway rule identifiable without reading the YAML.
	logArgs := []any{"cron", cronExpr}
	if next, interval, ok := cronCadence(cronExpr); ok {
		logArgs = append(logArgs, "nextRun", next.Format(time.RFC3339), "interval", interval.String())
	}
	app.logger.Info("registered schedule rule", logArgs...)

	return nil
}

// cronCadence reports the next fire time for an expression and the gap between
// that fire and the one after it.
//
// The gap is a true period only for uniform expressions ("*/5 * * * * *"). For a
// calendar schedule like "0 9 * * 1-5" it is simply the distance to the following
// run, which is the useful number to print anyway. ok is false when the
// expression does not parse, in which case the caller just omits the fields —
// gocron has already accepted the job by this point, so this is a logging
// nicety, never a validation path.
func cronCadence(cronExpr string) (time.Time, time.Duration, bool) {
	// rule.CronParser also handles the CRON_TZ= prefix this function may be
	// handed, and is the same option set gocron parses with.
	schedule, err := rule.CronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, 0, false
	}

	next := schedule.Next(time.Now())
	if next.IsZero() {
		return time.Time{}, 0, false
	}

	following := schedule.Next(next)
	if following.IsZero() {
		return next, 0, false
	}

	return next, following.Sub(next), true
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

	outcome, err := app.processor.ProcessSchedule(r)
	if err != nil {
		app.logger.Error("failed to process schedule rule",
			"cron", r.Trigger.Schedule.Cron,
			"error", err)
		return
	}

	if outcome.Empty() {
		app.logger.Debug("schedule rule produced no actions (conditions not met)",
			"cron", r.Trigger.Schedule.Cron)
		return
	}

	// Trailing-throttle batches fire when their window closes.
	for _, batch := range outcome.Deferred {
		app.coalescer.Submit(batch)
	}

	for _, action := range outcome.Immediate {
		if action.NATS != nil {
			ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
			if err := app.base.Broker.Publish(ctx, action.NATS); err != nil {
				app.logger.Error("failed to publish scheduled NATS action",
					"subject", action.NATS.Subject,
					"error", err)
				if app.metrics != nil {
					app.metrics.IncActionsTotal("error")
					app.metrics.IncActionPublishFailures()
				}
			} else {
				app.logger.Info("published scheduled NATS action",
					"subject", action.NATS.Subject)
				if app.metrics != nil {
					app.metrics.IncActionsTotal("success")
				}
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
					app.metrics.IncActionsTotal("error")
					app.metrics.IncActionPublishFailures()
				}
			} else {
				app.logger.Info("executed scheduled HTTP action",
					"url", action.HTTP.URL,
					"method", action.HTTP.Method)
				if app.metrics != nil {
					app.metrics.IncActionsTotal("success")
				}
			}
			cancel()
		}
	}
}

// Run starts the scheduler and waits for shutdown signal.
func (app *SchedulerApp) Run(ctx context.Context) error {
	scheduleRules := app.processor.ScheduleRules()

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
	}

	app.logger.Info("shutdown complete")
	return nil
}

// Close gracefully shuts down scheduler-specific resources.
// Shared resources (metrics, broker, logger) are cleaned up by BaseApp.Close().
func (app *SchedulerApp) Close() error {
	app.logger.Info("closing scheduler components")

	// Flush pending trailing-throttle batches before BaseApp closes the broker.
	flushCtx, cancel := context.WithTimeout(context.Background(), deferredFlushTimeout)
	defer cancel()
	app.coalescer.Stop(flushCtx)

	return nil
}

// executeDeferred runs one trailing-throttle action from a cron rule, using the
// same publish and HTTP paths as the immediate route.
func (app *SchedulerApp) executeDeferred(ctx context.Context, action *rule.Action) error {
	switch {
	case action.NATS != nil:
		if err := app.base.Broker.Publish(ctx, action.NATS); err != nil {
			if app.metrics != nil {
				app.metrics.IncActionsTotal("error")
				app.metrics.IncActionPublishFailures()
			}
			return fmt.Errorf("failed to publish deferred scheduled NATS action to %s: %w", action.NATS.Subject, err)
		}
		if app.metrics != nil {
			app.metrics.IncActionsTotal("success")
		}

	case action.HTTP != nil:
		if err := app.httpExecutor.ExecuteHTTPAction(ctx, action.HTTP); err != nil {
			if app.metrics != nil {
				app.metrics.IncActionsTotal("error")
				app.metrics.IncActionPublishFailures()
			}
			return fmt.Errorf("failed to execute deferred scheduled HTTP action to %s: %w", action.HTTP.URL, err)
		}
		if app.metrics != nil {
			app.metrics.IncActionsTotal("success")
		}
	}

	return nil
}
