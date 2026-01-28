// file: internal/broker/policy_test.go

package broker

import (
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"rule-router/config"
	"rule-router/internal/logger"
)

// newTestBroker creates a minimal NATSBroker for testing policy parsing functions
func newTestBroker() *NATSBroker {
	return &NATSBroker{
		logger: logger.NewNopLogger(),
		config: &config.Config{
			NATS: config.NATSConfig{
				Consumers: config.ConsumerConfig{
					ConsumerPrefix: "test",
				},
			},
		},
	}
}

func TestParseDeliverPolicy(t *testing.T) {
	broker := newTestBroker()

	tests := []struct {
		name       string
		policy     string
		want       jetstream.DeliverPolicy
		wantErr    bool
	}{
		{"all policy", "all", jetstream.DeliverAllPolicy, false},
		{"new policy", "new", jetstream.DeliverNewPolicy, false},
		{"last policy", "last", jetstream.DeliverLastPolicy, false},
		{"by_start_time policy", "by_start_time", jetstream.DeliverByStartTimePolicy, false},
		{"by_start_sequence policy", "by_start_sequence", jetstream.DeliverByStartSequencePolicy, false},
		{"unknown policy", "invalid", jetstream.DeliverAllPolicy, true},
		{"empty policy", "", jetstream.DeliverAllPolicy, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := broker.parseDeliverPolicy(tt.policy)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseDeliverPolicy(%q) expected error, got nil", tt.policy)
				}
				return
			}

			if err != nil {
				t.Errorf("parseDeliverPolicy(%q) unexpected error: %v", tt.policy, err)
				return
			}

			if got != tt.want {
				t.Errorf("parseDeliverPolicy(%q) = %v, want %v", tt.policy, got, tt.want)
			}
		})
	}
}

func TestParseReplayPolicy(t *testing.T) {
	broker := newTestBroker()

	tests := []struct {
		name    string
		policy  string
		want    jetstream.ReplayPolicy
		wantErr bool
	}{
		{"instant policy", "instant", jetstream.ReplayInstantPolicy, false},
		{"original policy", "original", jetstream.ReplayOriginalPolicy, false},
		{"unknown policy", "invalid", jetstream.ReplayInstantPolicy, true},
		{"empty policy", "", jetstream.ReplayInstantPolicy, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := broker.parseReplayPolicy(tt.policy)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseReplayPolicy(%q) expected error, got nil", tt.policy)
				}
				return
			}

			if err != nil {
				t.Errorf("parseReplayPolicy(%q) unexpected error: %v", tt.policy, err)
				return
			}

			if got != tt.want {
				t.Errorf("parseReplayPolicy(%q) = %v, want %v", tt.policy, got, tt.want)
			}
		})
	}
}

func TestGenerateConsumerName(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		subject string
		want    string
	}{
		{
			name:    "simple subject",
			prefix:  "rr",
			subject: "sensors.temperature",
			want:    "rr-sensors-temperature",
		},
		{
			name:    "single wildcard",
			prefix:  "rr",
			subject: "sensors.*",
			want:    "rr-sensors-wildcard",
		},
		{
			name:    "multi wildcard",
			prefix:  "rr",
			subject: "sensors.>",
			want:    "rr-sensors-multi-wildcard",
		},
		{
			name:    "mixed wildcards",
			prefix:  "rr",
			subject: "sensors.*.>",
			want:    "rr-sensors-wildcard-multi-wildcard",
		},
		{
			name:    "single token",
			prefix:  "myapp",
			subject: "events",
			want:    "myapp-events",
		},
		{
			name:    "deep nesting",
			prefix:  "test",
			subject: "a.b.c.d.e",
			want:    "test-a-b-c-d-e",
		},
		{
			name:    "subject with spaces",
			prefix:  "rr",
			subject: "some subject",
			want:    "rr-some-subject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broker := &NATSBroker{
				config: &config.Config{
					NATS: config.NATSConfig{
						Consumers: config.ConsumerConfig{
							ConsumerPrefix: tt.prefix,
						},
					},
				},
			}

			got := broker.generateConsumerName(tt.subject)
			if got != tt.want {
				t.Errorf("generateConsumerName(%q) with prefix %q = %q, want %q",
					tt.subject, tt.prefix, got, tt.want)
			}
		})
	}
}

