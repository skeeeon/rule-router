package rule

import (
	"strings"
	"testing"
	"time"
)

// TestCronParser_AcceptedDialect pins the expressions schedule triggers accept.
// The option set has to match the one gocron builds for withSeconds jobs; a rule
// the loader accepts but gocron cannot run would validate at startup and fail at
// its first fire, and a rule gocron would run but the loader rejects loses the
// only useful error message in the system.
func TestCronParser_AcceptedDialect(t *testing.T) {
	valid := []struct {
		name string
		expr string
	}{
		{"5-field standard", "0 8 * * 1-5"},
		{"5-field step", "*/5 * * * *"},
		{"6-field seconds", "*/5 * * * * *"},
		{"6-field every second", "* * * * * *"},
		{"6-field explicit second", "30 * * * * *"},
		{"descriptor hourly", "@hourly"},
		{"descriptor every duration", "@every 1h30m"},
		{"CRON_TZ prefix, 5-field", "CRON_TZ=America/New_York 0 8 * * 1-5"},
		{"CRON_TZ prefix, 6-field", "CRON_TZ=America/New_York */5 * * * * *"},
	}

	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := CronParser.Parse(tt.expr); err != nil {
				t.Errorf("CronParser.Parse(%q) = %v, want no error", tt.expr, err)
			}
		})
	}

	invalid := []struct {
		name string
		expr string
	}{
		{"too many fields", "* * * * * * *"},
		{"garbage", "not a cron"},
		{"minute out of range", "99 * * * *"},
		{"second out of range", "60 * * * * *"},
		{"empty", ""},
	}

	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := CronParser.Parse(tt.expr); err == nil {
				t.Errorf("CronParser.Parse(%q) succeeded, want an error", tt.expr)
			}
		})
	}
}

// TestCronParser_FiveFieldSemanticsUnchanged is the regression guard for the
// SecondOptional switch: adding an optional leading field must not shift what an
// existing 5-field expression means. "*/5 * * * *" stays every five minutes and
// must not silently become every five seconds.
func TestCronParser_FiveFieldSemanticsUnchanged(t *testing.T) {
	tests := []struct {
		expr string
		want time.Duration
	}{
		{"*/5 * * * *", 5 * time.Minute},
		{"* * * * *", time.Minute},
		{"0 * * * *", time.Hour},
		{"*/5 * * * * *", 5 * time.Second},
		{"* * * * * *", time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			schedule, err := CronParser.Parse(tt.expr)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tt.expr, err)
			}

			// Start from a whole minute so the first Next() lands on a boundary
			// and the gap to the one after it is the true period.
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			first := schedule.Next(base)
			second := schedule.Next(first)

			if got := second.Sub(first); got != tt.want {
				t.Errorf("%q fires every %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

// TestValidateScheduleTrigger covers the loader-level path, including the
// acceptance requirement that a malformed expression is rejected at load rather
// than at first fire.
func TestValidateScheduleTrigger(t *testing.T) {
	loader := newTestLoader()

	tests := []struct {
		name     string
		schedule *ScheduleTrigger
		errMsg   string // empty means the schedule must validate
	}{
		{
			name:     "5-field",
			schedule: &ScheduleTrigger{Cron: "0 8 * * 1-5"},
		},
		{
			name:     "6-field seconds",
			schedule: &ScheduleTrigger{Cron: "*/5 * * * * *"},
		},
		{
			name:     "6-field with timezone",
			schedule: &ScheduleTrigger{Cron: "*/5 * * * * *", Timezone: "America/New_York"},
		},
		{
			name:     "descriptor",
			schedule: &ScheduleTrigger{Cron: "@hourly"},
		},
		{
			name:     "empty cron",
			schedule: &ScheduleTrigger{Cron: ""},
			errMsg:   "cron expression cannot be empty",
		},
		{
			name:     "malformed 6-field rejected at load",
			schedule: &ScheduleTrigger{Cron: "*/5 * * * * bogus"},
			errMsg:   "invalid cron expression",
		},
		{
			name:     "seven fields rejected at load",
			schedule: &ScheduleTrigger{Cron: "* * * * * * *"},
			errMsg:   "invalid cron expression",
		},
		{
			name:     "invalid timezone",
			schedule: &ScheduleTrigger{Cron: "*/5 * * * * *", Timezone: "Mars/Olympus_Mons"},
			errMsg:   "invalid timezone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loader.validateScheduleTrigger(tt.schedule)

			if tt.errMsg == "" {
				if err != nil {
					t.Fatalf("validateScheduleTrigger(%+v) = %v, want no error", tt.schedule, err)
				}
				return
			}

			if err == nil {
				t.Fatalf("validateScheduleTrigger(%+v) succeeded, want error containing %q", tt.schedule, tt.errMsg)
			}
			if !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.errMsg)
			}
		})
	}
}
