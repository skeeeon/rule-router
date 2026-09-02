package app

import (
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"

	"rule-router/internal/logger"
	"rule-router/internal/metrics"
)

// TestGocronAcceptsSecondsField checks the dependency itself rather than our
// wrapper. Sub-minute scheduling rests entirely on gocron's withSeconds parser
// being SecondOptional-based: if a future gocron release switched it to Second,
// every existing 5-field rule in the wild would silently reinterpret its minute
// field as seconds and start firing 60x too often. That is the failure this test
// exists to catch, so it asserts on cadence, not on parse success.
//
// gocron only computes run times once the scheduler is started, so the test
// starts one and reads the schedule rather than waiting for fires — no sleeps,
// and the assertion is exact instead of timing-dependent. The tasks are no-ops.
func TestGocronAcceptsSecondsField(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want time.Duration
	}{
		{"5-field is still minutes", "*/5 * * * *", 5 * time.Minute},
		{"5-field every minute", "* * * * *", time.Minute},
		{"6-field seconds", "*/5 * * * * *", 5 * time.Second},
		{"6-field every second", "* * * * * *", time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := gocron.NewScheduler()
			if err != nil {
				t.Fatalf("NewScheduler: %v", err)
			}
			defer func() { _ = s.Shutdown() }()

			job, err := s.NewJob(
				gocron.CronJob(tt.expr, true),
				gocron.NewTask(func() {}),
			)
			if err != nil {
				t.Fatalf("NewJob(%q) failed: %v", tt.expr, err)
			}

			s.Start()

			runs, err := job.NextRuns(2)
			if err != nil {
				t.Fatalf("NextRuns: %v", err)
			}
			if len(runs) < 2 {
				t.Fatalf("got %d next runs, want 2", len(runs))
			}

			if got := runs[1].Sub(runs[0]); got != tt.want {
				t.Errorf("%q fires every %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

// TestGocronRejectsMalformedExpression confirms the loader is not the only line
// of defence being relied on, and that a 7-field expression is an error rather
// than a silently truncated schedule.
func TestGocronRejectsMalformedExpression(t *testing.T) {
	s, err := gocron.NewScheduler()
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	defer func() { _ = s.Shutdown() }()

	for _, expr := range []string{"* * * * * * *", "*/5 * * * * bogus", "not a cron"} {
		if _, err := s.NewJob(gocron.CronJob(expr, true), gocron.NewTask(func() {})); err == nil {
			t.Errorf("NewJob(%q) succeeded, want an error", expr)
		}
	}
}

func TestCronCadence(t *testing.T) {
	t.Run("reports the interval of a uniform expression", func(t *testing.T) {
		tests := []struct {
			expr string
			want time.Duration
		}{
			{"*/5 * * * *", 5 * time.Minute},
			{"*/5 * * * * *", 5 * time.Second},
			{"* * * * * *", time.Second},
			{"0 * * * *", time.Hour},
		}

		for _, tt := range tests {
			next, interval, ok := cronCadence(tt.expr)
			if !ok {
				t.Errorf("cronCadence(%q) not ok, want ok", tt.expr)
				continue
			}
			if interval != tt.want {
				t.Errorf("cronCadence(%q) interval = %v, want %v", tt.expr, interval, tt.want)
			}
			if next.Before(time.Now().Add(-time.Second)) {
				t.Errorf("cronCadence(%q) next = %v, want a future time", tt.expr, next)
			}
		}
	})

	// registerScheduleRule hands this function the CRON_TZ-prefixed string, so
	// the timezone has to survive. This is acceptance criterion 3: a 6-field
	// expression with a timezone still honours the zone.
	t.Run("honours a CRON_TZ prefix on a 6-field expression", func(t *testing.T) {
		ny, err := time.LoadLocation("America/New_York")
		if err != nil {
			t.Skipf("tzdata unavailable: %v", err)
		}

		next, interval, ok := cronCadence("CRON_TZ=America/New_York 30 8 * * *")
		if !ok {
			t.Fatal("cronCadence not ok, want ok")
		}
		if interval != 24*time.Hour {
			t.Errorf("interval = %v, want 24h", interval)
		}
		if h, m, _ := next.In(ny).Clock(); h != 8 || m != 30 {
			t.Errorf("next run is %02d:%02d New York time, want 08:30", h, m)
		}

		// And the same with a seconds field present.
		nextSec, intervalSec, ok := cronCadence("CRON_TZ=America/New_York */10 * * * * *")
		if !ok {
			t.Fatal("cronCadence with seconds not ok, want ok")
		}
		if intervalSec != 10*time.Second {
			t.Errorf("interval = %v, want 10s", intervalSec)
		}
		if nextSec.IsZero() {
			t.Error("next run is zero")
		}
	})

	t.Run("reports not-ok for an unparseable expression", func(t *testing.T) {
		for _, expr := range []string{"", "not a cron", "* * * * * * *"} {
			if _, _, ok := cronCadence(expr); ok {
				t.Errorf("cronCadence(%q) ok, want not ok", expr)
			}
		}
	})
}

// TestScheduleMonitor_WarnsOncePerSchedule pins the log-spam guard. A rule firing
// every second against a slower action drops a fire roughly every second; warning
// on each one would bury the message it is meant to surface.
func TestScheduleMonitor_WarnsOncePerSchedule(t *testing.T) {
	countWarned := func(sm *scheduleMonitor) int {
		n := 0
		sm.warned.Range(func(_, _ any) bool {
			n++
			return true
		})
		return n
	}

	sm := newScheduleMonitor(logger.NewNop(), nil)

	// Repeated drops on one schedule warn once.
	for i := 0; i < 5; i++ {
		sm.IncrementJob(uuid.Nil, "*/1 * * * * *", nil, gocron.SingletonRescheduled)
	}
	if got := countWarned(sm); got != 1 {
		t.Errorf("after 5 drops on one schedule, warned %d schedules, want 1", got)
	}

	// A different schedule gets its own warning.
	sm.IncrementJob(uuid.Nil, "*/5 * * * * *", nil, gocron.SingletonRescheduled)
	if got := countWarned(sm); got != 2 {
		t.Errorf("after a drop on a second schedule, warned %d schedules, want 2", got)
	}

	// Normal outcomes never warn.
	sm.IncrementJob(uuid.Nil, "0 8 * * *", nil, gocron.Success)
	sm.IncrementJob(uuid.Nil, "0 9 * * *", nil, gocron.Fail)
	if got := countWarned(sm); got != 2 {
		t.Errorf("success/fail added warnings: warned %d schedules, want 2", got)
	}
}

// TestScheduleMonitor_NilMetrics guards the path taken when metrics are disabled;
// the monitor is installed unconditionally, so it must tolerate a nil collector.
func TestScheduleMonitor_NilMetrics(t *testing.T) {
	sm := newScheduleMonitor(logger.NewNop(), (*metrics.Metrics)(nil))
	sm.metrics = nil

	sm.IncrementJob(uuid.Nil, "* * * * *", nil, gocron.Success)
	sm.RecordJobTiming(time.Now(), time.Now().Add(time.Second), uuid.Nil, "* * * * *", nil)
}
