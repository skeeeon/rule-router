package app

import (
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

// scheduleMonitor implements gocron.Monitor so that dropped fires are visible.
//
// Every job is registered with WithSingletonMode(LimitModeReschedule): when a
// fire comes due while the previous one is still running, gocron discards it and
// waits for the next. Without a monitor that discard is completely silent — no
// log, no error, no metric. That was tolerable when the tightest schedule was
// one minute; it is not once a rule can fire every second, because an action
// slower than its interval then drops most of its fires and the only symptom is
// missing data downstream. gocron reports the discard as SingletonRescheduled.
//
// The job name is the rule's raw cron expression (set by registerScheduleRule),
// which keeps label cardinality bounded by the number of distinct schedules.
type scheduleMonitor struct {
	logger  *logger.Logger
	metrics *metrics.Metrics

	// warned records which schedules have already logged a drop. A rule firing
	// every second against a slow action drops roughly once a second, and a warn
	// per drop would bury the log it is meant to draw attention to. One line per
	// schedule says what is wrong; the counter carries the ongoing rate.
	warned sync.Map
}

var _ gocron.Monitor = (*scheduleMonitor)(nil)

func newScheduleMonitor(log *logger.Logger, m *metrics.Metrics) *scheduleMonitor {
	return &scheduleMonitor{logger: log, metrics: m}
}

// IncrementJob is called by gocron once per fire outcome: success, fail, or
// singleton_rescheduled (dropped).
func (sm *scheduleMonitor) IncrementJob(_ uuid.UUID, name string, _ []string, status gocron.JobStatus) {
	if sm.metrics != nil {
		sm.metrics.IncSchedulerJobRun(name, string(status))
	}

	if status != gocron.SingletonRescheduled {
		return
	}

	if _, alreadyWarned := sm.warned.LoadOrStore(name, struct{}{}); alreadyWarned {
		return
	}

	sm.logger.Warn("scheduled fire dropped: previous run of this rule was still in progress",
		"cron", name,
		"hint", "the action takes longer than the cron interval; subsequent drops are counted by scheduler_job_runs_total{status=\"singleton_rescheduled\"} and not logged again")
}

// RecordJobTiming records how long a fire took. Compared against the interval
// logged at registration, this is what explains a drop rather than just
// reporting it.
func (sm *scheduleMonitor) RecordJobTiming(startTime, endTime time.Time, _ uuid.UUID, name string, _ []string) {
	if sm.metrics != nil {
		sm.metrics.ObserveSchedulerJobDuration(name, endTime.Sub(startTime).Seconds())
	}
}
