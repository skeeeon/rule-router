// file: internal/app/scheduler.go

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/go-co-op/gocron/v2"
	"rule-router/config"
	"rule-router/internal/broker"
	"rule-router/internal/lifecycle"
	"rule-router/internal/logger"
	"rule-router/internal/metrics"
	"rule-router/internal/rule"
)

// Timeout constants for SchedulerApp operations
const (
	// schedulerMetricsShutdownTimeout is the maximum time to wait for the metrics server to shutdown
	schedulerMetricsShutdownTimeout = 5 * time.Second

	// publishTimeout is the maximum time to wait for a single publish operation
	publishTimeout = 10 * time.Second
)

// Verify SchedulerApp implements lifecycle.Application interface at compile time
var _ lifecycle.Application = (*SchedulerApp)(nil)

// SchedulerApp represents the rule-scheduler application that publishes messages on cron schedules
type SchedulerApp struct {
	config           *config.Config
	logger           *logger.Logger
	metrics          *metrics.Metrics
	processor        *rule.Processor
	broker           *broker.NATSBroker
	base             *BaseApp
	scheduler        gocron.Scheduler
	metricsCollector *metrics.MetricsCollector
}

// NewSchedulerApp creates a new rule-scheduler application instance using the pre-built base components.
func NewSchedulerApp(base *BaseApp, cfg *config.Config) (*SchedulerApp, error) {
	app := &SchedulerApp{
		config:           cfg,
		logger:           base.Logger,
		metrics:          base.Metrics,
		processor:        base.Processor,
		broker:           base.Broker,
		base:             base,
		metricsCollector: base.Collector,
	}

	scheduleRules := app.processor.GetScheduleRules()
	if len(scheduleRules) == 0 {
		return nil, fmt.Errorf("no schedule-triggered rules found")
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

	return app, nil
}

// registerScheduleRule registers a single schedule-triggered rule as a cron job
func (app *SchedulerApp) registerScheduleRule(r *rule.Rule) error {
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
	)
	if err != nil {
		return fmt.Errorf("failed to register cron job: %w", err)
	}

	app.logger.Info("registered schedule rule",
		"cron", cronExpr)

	return nil
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
			if err := app.broker.Publish(ctx, action.NATS); err != nil {
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
			app.logger.Warn("HTTP actions are not yet supported in schedule rules",
				"url", action.HTTP.URL,
				"method", action.HTTP.Method)
		}
	}
}

// Run starts the scheduler and waits for shutdown signal.
func (app *SchedulerApp) Run(ctx context.Context) error {
	scheduleRules := app.processor.GetScheduleRules()

	app.logger.Info("configuration summary",
		"scheduleRules", len(scheduleRules),
		"kvEnabled", app.config.KV.Enabled,
		"kvBuckets", app.config.KV.Buckets,
		"publishMode", app.config.NATS.Publish.Mode)

	app.logger.Info("starting rule-scheduler",
		"natsUrls", app.config.NATS.URLs,
		"metricsEnabled", app.config.Metrics.Enabled)

	// Start the cron scheduler
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

// Close gracefully shuts down all application components
func (app *SchedulerApp) Close() error {
	app.logger.Info("closing application components")

	var errors []error

	// Stop metrics collector
	if app.metricsCollector != nil {
		app.metricsCollector.Stop()
	}

	// Shutdown metrics server and wait for goroutine to exit
	if app.base != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), schedulerMetricsShutdownTimeout)
		defer cancel()
		if err := app.base.ShutdownMetricsServer(shutdownCtx); err != nil {
			errors = append(errors, err)
		}
	}

	// Close NATS broker
	if app.broker != nil {
		if err := app.broker.Close(); err != nil {
			errors = append(errors, fmt.Errorf("failed to close NATS broker: %w", err))
		}
	}

	// Sync logger
	if app.logger != nil {
		if err := app.logger.Sync(); err != nil {
			app.logger.Debug("logger sync completed", "error", err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %v", errors)
	}

	return nil
}
