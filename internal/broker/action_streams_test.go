// file: internal/broker/action_streams_test.go

package broker

import (
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"rule-router/internal/rule"
)

// natsActionRule builds a rule whose NATS action publishes to subject.
func natsActionRule(subject string, mutate func(*rule.NATSAction)) *rule.Rule {
	action := &rule.NATSAction{Subject: subject, Payload: "{}"}
	if mutate != nil {
		mutate(action)
	}
	return &rule.Rule{
		Trigger: rule.Trigger{Schedule: &rule.ScheduleTrigger{Cron: "*/1 * * * *"}},
		Action:  rule.Action{NATS: action},
	}
}

// TestJetStreamActionSubjects covers which action subjects are candidates for
// the unstreamed-subject warning. Everything skipped here is skipped because it
// is not a JetStream publish to a statically known subject.
func TestJetStreamActionSubjects(t *testing.T) {
	tests := []struct {
		name       string
		globalMode string
		rules      []*rule.Rule
		want       []string
	}{
		{
			name:       "plain action inherits jetstream default",
			globalMode: rule.ModeJetStream,
			rules:      []*rule.Rule{natsActionRule("out.events", nil)},
			want:       []string{"out.events"},
		},
		{
			name:       "core action is not a stream publish",
			globalMode: rule.ModeJetStream,
			rules: []*rule.Rule{natsActionRule("device.heartbeat", func(a *rule.NATSAction) {
				a.Mode = rule.ModeCore
			})},
			want: nil,
		},
		{
			name:       "global core mode skips inheriting actions",
			globalMode: rule.ModeCore,
			rules:      []*rule.Rule{natsActionRule("out.events", nil)},
			want:       nil,
		},
		{
			name:       "explicit jetstream survives a global core default",
			globalMode: rule.ModeCore,
			rules: []*rule.Rule{natsActionRule("out.events", func(a *rule.NATSAction) {
				a.Mode = rule.ModeJetStream
			})},
			// An action that opts into JetStream is checkable regardless of the
			// global default. Deployments with no streams at all are covered by
			// UnstreamedSubjects' empty-stream guard, not by skipping them here.
			want: []string{"out.events"},
		},
		{
			name:       "request/reply uses core transport",
			globalMode: rule.ModeJetStream,
			rules: []*rule.Rule{natsActionRule("service.query", func(a *rule.NATSAction) {
				a.Request = true
			})},
			want: nil,
		},
		{
			name:       "templated subject is unknowable until evaluation",
			globalMode: rule.ModeJetStream,
			rules:      []*rule.Rule{natsActionRule("alerts.{level}.raised", nil)},
			want:       nil,
		},
		{
			name:       "duplicates collapse",
			globalMode: rule.ModeJetStream,
			rules: []*rule.Rule{
				natsActionRule("out.events", nil),
				natsActionRule("out.events", nil),
				natsActionRule("out.other", nil),
			},
			want: []string{"out.events", "out.other"},
		},
		{
			name:       "http publishResponse is a stream publish too",
			globalMode: rule.ModeJetStream,
			rules: []*rule.Rule{{
				Trigger: rule.Trigger{Schedule: &rule.ScheduleTrigger{Cron: "*/5 * * * *"}},
				Action: rule.Action{HTTP: &rule.HTTPAction{
					URL: "https://example.test/poll", Method: "GET",
					PublishResponse: &rule.PublishResponseSpec{Subject: "poll.result"},
				}},
			}},
			want: []string{"poll.result"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jetStreamActionSubjects(tt.rules, tt.globalMode)
			if len(got) != len(tt.want) {
				t.Fatalf("jetStreamActionSubjects() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("jetStreamActionSubjects() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestUnstreamedSubjects verifies the coverage check, including the guard that
// keeps an empty stream list from condemning every subject.
func TestUnstreamedSubjects(t *testing.T) {
	streams := []StreamInfo{
		{Name: "SENSORS", Subjects: []string{"sensors.>"}, Storage: jetstream.MemoryStorage},
		{Name: "KV_twin", Subjects: []string{"$KV.twin.>"}, Storage: jetstream.FileStorage},
	}

	t.Run("reports only uncovered subjects, in order", func(t *testing.T) {
		sr := newTestResolverWithStreams(streams)
		got := sr.UnstreamedSubjects([]string{
			"sensors.hq.temp",
			"camera.hq.lobby.heartbeat",
			"$KV.twin.thing.x.online",
			"access.hq.door.evt",
		})
		want := []string{"camera.hq.lobby.heartbeat", "access.hq.door.evt"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("UnstreamedSubjects() = %v, want %v", got, want)
		}
	})

	t.Run("silent when no streams are visible", func(t *testing.T) {
		sr := newTestResolverWithStreams(nil)
		if got := sr.UnstreamedSubjects([]string{"camera.hq.lobby.heartbeat"}); got != nil {
			t.Fatalf("UnstreamedSubjects() = %v, want nil when no streams are known", got)
		}
	})

	t.Run("silent before discovery", func(t *testing.T) {
		sr := newTestResolverWithStreams(streams)
		sr.discovered = false
		if got := sr.UnstreamedSubjects([]string{"camera.hq.lobby.heartbeat"}); got != nil {
			t.Fatalf("UnstreamedSubjects() = %v, want nil before discovery", got)
		}
	})
}
